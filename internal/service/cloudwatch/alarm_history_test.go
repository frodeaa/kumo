package cloudwatch

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

const (
	testHistoryAlarm = "history-alarm"
	summaryAlarmToOK = "Alarm updated from ALARM to OK"
	summaryCreated   = `Alarm "history-alarm" created`
	summaryUpdated   = `Alarm "history-alarm" updated`
)

type historyItem struct {
	AlarmName       string
	AlarmType       string
	Timestamp       float64
	HistoryItemType string
	HistorySummary  string
	HistoryData     string
}

type historyPage struct {
	AlarmHistoryItems []historyItem
	NextToken         string
}

func describeHistory(t *testing.T, svc *Service, body string) historyPage {
	t.Helper()

	code, resp := postJSON(t, svc, actionDescribeAlarmHistory, body)
	if code != http.StatusOK {
		t.Fatalf("DescribeAlarmHistory %s: code=%d body=%s", body, code, resp)
	}

	var page historyPage
	if err := json.Unmarshal([]byte(resp), &page); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, resp)
	}

	return page
}

func setAlarmState(t *testing.T, svc *Service, state string) {
	t.Helper()

	code, body := postJSON(t, svc, actionSetAlarmState,
		`{"AlarmName": "`+testHistoryAlarm+`", "StateValue": "`+state+`", "StateReason": "set to `+state+`"}`)
	if code != http.StatusOK {
		t.Fatalf("SetAlarmState %s: code=%d body=%s", state, code, body)
	}
}

// newAlarmWithStateChanges creates the alarm and moves it to ALARM and back
// to OK, recording one ConfigurationUpdate and two StateUpdate items.
func newAlarmWithStateChanges(t *testing.T) *Service {
	t.Helper()

	svc := New(NewMemoryStorage(""))
	putTestAlarm(t, svc, testHistoryAlarm)
	setAlarmState(t, svc, "ALARM")
	setAlarmState(t, svc, "OK")

	return svc
}

func newStateValue(t *testing.T, historyData string) string {
	t.Helper()

	var data struct {
		Version  string `json:"version"`
		NewState struct {
			StateValue string `json:"stateValue"`
		} `json:"newState"`
	}

	if err := json.Unmarshal([]byte(historyData), &data); err != nil {
		t.Fatalf("HistoryData is not JSON: %v (%s)", err, historyData)
	}

	if data.Version != alarmHistoryDataVersion {
		t.Errorf("HistoryData version = %q", data.Version)
	}

	return data.NewState.StateValue
}

func TestDescribeAlarmHistory_StateUpdatesNewestFirst(t *testing.T) {
	svc := newAlarmWithStateChanges(t)

	page := describeHistory(t, svc, `{"AlarmName": "`+testHistoryAlarm+`", "HistoryItemType": "StateUpdate"}`)
	if len(page.AlarmHistoryItems) != 2 {
		t.Fatalf("items = %+v, want 2 state updates", page.AlarmHistoryItems)
	}

	newest, oldest := page.AlarmHistoryItems[0], page.AlarmHistoryItems[1]
	if got := newStateValue(t, newest.HistoryData); got != "OK" {
		t.Errorf("newest newState = %q, want OK", got)
	}

	if got := newStateValue(t, oldest.HistoryData); got != "ALARM" {
		t.Errorf("oldest newState = %q, want ALARM", got)
	}

	if newest.HistorySummary != summaryAlarmToOK || newest.AlarmType != alarmTypeMetricAlarm || newest.Timestamp <= 0 {
		t.Errorf("newest item = %+v", newest)
	}
}

func TestDescribeAlarmHistory_IncludesConfigurationUpdates(t *testing.T) {
	svc := newAlarmWithStateChanges(t)

	page := describeHistory(t, svc, `{"AlarmName": "`+testHistoryAlarm+`", "ScanBy": "TimestampAscending"}`)
	if len(page.AlarmHistoryItems) != 3 {
		t.Fatalf("items = %+v, want 3", page.AlarmHistoryItems)
	}

	created := page.AlarmHistoryItems[0]
	if created.HistoryItemType != historyItemConfigurationUpdate || created.HistorySummary != summaryCreated {
		t.Errorf("first item = %+v, want the creation", created)
	}

	if !strings.Contains(created.HistoryData, `"type":"Create"`) || !strings.Contains(created.HistoryData, `"createdAlarm":{"alarmName":"history-alarm"`) {
		t.Errorf("creation HistoryData = %s", created.HistoryData)
	}

	if got := newStateValue(t, page.AlarmHistoryItems[2].HistoryData); got != "OK" {
		t.Errorf("last item newState = %q, want OK", got)
	}
}

func TestDescribeAlarmHistory_PagesWithMaxRecords(t *testing.T) {
	svc := newAlarmWithStateChanges(t)

	var summaries []string

	nextToken := ""

	for range 5 {
		page := describeHistory(t, svc, `{"MaxRecords": 1, "NextToken": "`+nextToken+`"}`)
		if len(page.AlarmHistoryItems) != 1 {
			t.Fatalf("page = %+v, want 1 item", page)
		}

		summaries = append(summaries, page.AlarmHistoryItems[0].HistorySummary)

		if page.NextToken == "" {
			break
		}

		nextToken = page.NextToken
	}

	want := []string{summaryAlarmToOK, "Alarm updated from INSUFFICIENT_DATA to ALARM", summaryCreated}
	if strings.Join(summaries, "|") != strings.Join(want, "|") {
		t.Errorf("summaries across pages = %q, want %q", summaries, want)
	}
}

func TestDescribeAlarmHistory_FiltersByDateRange(t *testing.T) {
	svc := newAlarmWithStateChanges(t)
	hourAgo := time.Now().Add(-time.Hour).Unix()
	inAnHour := time.Now().Add(time.Hour).Unix()

	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "window around now", body: `{"StartDate": ` + strconv.FormatInt(hourAgo, 10) + `, "EndDate": ` + strconv.FormatInt(inAnHour, 10) + `}`, want: 3},
		{name: "starts in the future", body: `{"StartDate": ` + strconv.FormatInt(inAnHour, 10) + `}`, want: 0},
		{name: "ends in the past", body: `{"EndDate": ` + strconv.FormatInt(hourAgo, 10) + `}`, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(describeHistory(t, svc, tt.body).AlarmHistoryItems); got != tt.want {
				t.Errorf("items = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDescribeAlarmHistory_SameStateRecordsNothing(t *testing.T) {
	svc := New(NewMemoryStorage(""))
	putTestAlarm(t, svc, testHistoryAlarm)
	setAlarmState(t, svc, "ALARM")
	setAlarmState(t, svc, "ALARM")

	page := describeHistory(t, svc, `{"AlarmName": "`+testHistoryAlarm+`", "HistoryItemType": "StateUpdate"}`)
	if len(page.AlarmHistoryItems) != 1 {
		t.Errorf("items = %+v, want one state update", page.AlarmHistoryItems)
	}
}

func TestDescribeAlarmHistory_UpdateKeepsStateAndIsRecorded(t *testing.T) {
	svc := New(NewMemoryStorage(""))
	putTestAlarm(t, svc, testHistoryAlarm)
	setAlarmState(t, svc, "ALARM")
	putTestAlarm(t, svc, testHistoryAlarm)

	code, body := postJSON(t, svc, actionDescribeAlarms, `{"AlarmNames": ["`+testHistoryAlarm+`"]}`)
	if code != http.StatusOK || !strings.Contains(body, `"StateValue":"ALARM"`) {
		t.Errorf("DescribeAlarms after update: code=%d body=%s, want the ALARM state kept", code, body)
	}

	page := describeHistory(t, svc, `{"HistoryItemType": "ConfigurationUpdate"}`)
	if len(page.AlarmHistoryItems) != 2 || page.AlarmHistoryItems[0].HistorySummary != summaryUpdated ||
		!strings.Contains(page.AlarmHistoryItems[0].HistoryData, `"type":"Update","updatedAlarm":`) {
		t.Errorf("configuration updates = %+v, want the update first", page.AlarmHistoryItems)
	}
}

func TestDescribeAlarmHistory_DeleteDropsHistory(t *testing.T) {
	svc := newAlarmWithStateChanges(t)

	code, body := postJSON(t, svc, actionDeleteAlarms, `{"AlarmNames": ["`+testHistoryAlarm+`"]}`)
	if code != http.StatusOK {
		t.Fatalf("DeleteAlarms: code=%d body=%s", code, body)
	}

	if items := describeHistory(t, svc, `{}`).AlarmHistoryItems; len(items) != 0 {
		t.Errorf("items after delete = %+v, want none", items)
	}
}

func TestDescribeAlarmHistory_UnknownAlarmIsEmpty(t *testing.T) {
	svc := New(NewMemoryStorage(""))

	code, body := postJSON(t, svc, actionDescribeAlarmHistory, `{"AlarmName": "missing"}`)
	if code != http.StatusOK || body != "{\"AlarmHistoryItems\":[]}\n" {
		t.Errorf("code=%d body=%q, want an empty list", code, body)
	}
}

func TestDescribeAlarmHistory_RejectsInvalidPaging(t *testing.T) {
	svc := New(NewMemoryStorage(""))

	for _, body := range []string{`{"MaxRecords": 0}`, `{"MaxRecords": 101}`, `{"NextToken": "bogus"}`} {
		if code, resp := postJSON(t, svc, actionDescribeAlarmHistory, body); code != http.StatusBadRequest {
			t.Errorf("%s: code=%d body=%s, want 400", body, code, resp)
		}
	}
}

func TestQueryProtocol_DescribeAlarmHistoryReturnsXML(t *testing.T) {
	svc := newAlarmWithStateChanges(t)

	rec := postQuery(t, svc, actionDescribeAlarmHistory, `{"AlarmName": "`+testHistoryAlarm+`", "HistoryItemType": "StateUpdate"}`)

	var resp struct {
		Items []struct {
			Timestamp   string `xml:"Timestamp"`
			HistoryData string `xml:"HistoryData"`
		} `xml:"DescribeAlarmHistoryResult>AlarmHistoryItems>member"`
	}

	if err := xml.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}

	if len(resp.Items) != 2 {
		t.Fatalf("items = %+v, want 2", resp.Items)
	}

	if _, err := time.Parse(time.RFC3339, resp.Items[0].Timestamp); err != nil {
		t.Errorf("Timestamp %q is not RFC 3339: %v", resp.Items[0].Timestamp, err)
	}

	if !strings.Contains(resp.Items[0].HistoryData, `"newState":{"stateValue":"OK"`) {
		t.Errorf("HistoryData = %s", resp.Items[0].HistoryData)
	}
}

func TestCBORProtocol_DescribeAlarmHistoryRoundTrip(t *testing.T) {
	svc := newAlarmWithStateChanges(t)

	body, err := cbor.Marshal(DescribeAlarmHistoryCBORRequest{AlarmName: testHistoryAlarm, HistoryItemType: historyItemStateUpdate})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/cbor")

	rec := httptest.NewRecorder()
	svc.DispatchCBORAction(rec, req, actionDescribeAlarmHistory)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}

	var resp DescribeAlarmHistoryCBORResponse
	if err := cbor.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(resp.AlarmHistoryItems) != 2 || resp.AlarmHistoryItems[0].Timestamp.IsZero() ||
		resp.AlarmHistoryItems[0].HistorySummary != summaryAlarmToOK {
		t.Errorf("items = %+v", resp.AlarmHistoryItems)
	}
}
