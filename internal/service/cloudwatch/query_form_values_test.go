package cloudwatch

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

const (
	testFormAction       = "Action"
	testFormVersion      = "Version"
	testQueryVersion     = "2010-08-01"
	testMetricLatency    = "Latency"
	testDimensionAccount = "Account"
	testStatSum          = "Sum"
)

// cliPutMetricAlarmForm is the form body aws-cli 2.27 sends for
// put-metric-alarm, copied from --debug.
var cliPutMetricAlarmForm = url.Values{
	testFormAction:              {actionPutMetricAlarm},
	testFormVersion:             {testQueryVersion},
	"AlarmName":                 {"cli-alarm"},
	"AlarmDescription":          {"123"},
	"ActionsEnabled":            {"true"},
	"MetricName":                {"ConsoleLoginFailure"},
	"Namespace":                 {"Kumo/Cli"},
	"Statistic":                 {testStatSum},
	"Dimensions.member.1.Name":  {testDimensionAccount},
	"Dimensions.member.1.Value": {"000000000000"},
	"Period":                    {"60"},
	"EvaluationPeriods":         {"1"},
	"Threshold":                 {"1.0"},
	"ComparisonOperator":        {"GreaterThanOrEqualToThreshold"},
}

func TestQueryProtocol_PutMetricAlarmFromCLIFormBody(t *testing.T) {
	svc := New(NewMemoryStorage(""))

	rec := postQueryForm(t, svc, cliPutMetricAlarmForm)
	if rec.Code != http.StatusOK {
		t.Fatalf("PutMetricAlarm: code=%d body=%s", rec.Code, rec.Body.String())
	}

	code, body := postJSON(t, svc, actionDescribeAlarms, `{"AlarmNames": ["cli-alarm"]}`)
	if code != http.StatusOK {
		t.Fatalf("DescribeAlarms: code=%d body=%s", code, body)
	}

	var resp DescribeAlarmsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}

	if len(resp.MetricAlarms) != 1 {
		t.Fatalf("alarms = %s", body)
	}

	alarm := resp.MetricAlarms[0]
	if alarm.AlarmDescription != "123" || alarm.Threshold != 1 || alarm.Period != 60 || !alarm.ActionsEnabled ||
		len(alarm.Dimensions) != 1 || alarm.Dimensions[0].Value != "000000000000" {
		t.Errorf("alarm = %+v", alarm)
	}
}

func TestQueryProtocol_PutMetricDataWithDecimalValueAndNumericDimension(t *testing.T) {
	svc := New(NewMemoryStorage(""))

	rec := postQueryForm(t, svc, url.Values{
		testFormAction:                   {actionPutMetricData},
		testFormVersion:                  {testQueryVersion},
		"Namespace":                      {"Kumo/Cli"},
		"MetricData.member.1.MetricName": {testMetricLatency},
		"MetricData.member.1.Value":      {"7.5"},
		"MetricData.member.1.Dimensions.member.1.Name":  {testDimensionAccount},
		"MetricData.member.1.Dimensions.member.1.Value": {"123456789012"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PutMetricData: code=%d body=%s", rec.Code, rec.Body.String())
	}

	code, body := postJSON(t, svc, actionGetMetricStatistics, `{
		"Namespace": "Kumo/Cli",
		"MetricName": "Latency",
		"Dimensions": [{"Name": "Account", "Value": "123456789012"}],
		"Period": 60,
		"Statistics": ["Sum"]
	}`)
	if code != http.StatusOK {
		t.Fatalf("GetMetricStatistics: code=%d body=%s", code, body)
	}

	var resp struct {
		Datapoints []struct{ Sum float64 }
	}

	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}

	if len(resp.Datapoints) != 1 || resp.Datapoints[0].Sum != 7.5 {
		t.Errorf("datapoints = %s, want Sum 7.5 under the numeric dimension", body)
	}
}
