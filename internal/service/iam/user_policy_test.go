package iam

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

const (
	paramUserName       = "UserName"
	paramPolicyName     = "PolicyName"
	paramPolicyDocument = "PolicyDocument"

	actionPutUserPolicy    = "PutUserPolicy"
	actionGetUserPolicy    = "GetUserPolicy"
	actionDeleteUserPolicy = "DeleteUserPolicy"
	actionListUserPolicies = "ListUserPolicies"

	testUnknownUser = "nobody"
	testUserName    = "dummy@dintero.com"
	testPolicyDoc   = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"*","Resource":"*"}]}`
)

// callIAM sends a form-encoded Query request, as the IAM-direct route
// receives it.
func callIAM(t *testing.T, svc *Service, action string, params url.Values) *httptest.ResponseRecorder {
	t.Helper()

	form := url.Values{"Action": {action}, "Version": {"2010-05-08"}}
	for k, v := range params {
		form[k] = v
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	svc.DispatchAction(rec, req)

	return rec
}

func mustCallIAM(t *testing.T, svc *Service, action string, params url.Values) *httptest.ResponseRecorder {
	t.Helper()

	rec := callIAM(t, svc, action, params)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: code=%d body=%s", action, rec.Code, rec.Body.String())
	}

	return rec
}

func newServiceWithUser(t *testing.T) *Service {
	t.Helper()

	svc := New(NewMemoryStorage())
	mustCallIAM(t, svc, "CreateUser", url.Values{paramUserName: {testUserName}})

	return svc
}

func putUserPolicy(t *testing.T, svc *Service, policyName, doc string) {
	t.Helper()

	mustCallIAM(t, svc, actionPutUserPolicy, url.Values{
		paramUserName:       {testUserName},
		paramPolicyName:     {policyName},
		paramPolicyDocument: {doc},
	})
}

func listUserPolicies(t *testing.T, svc *Service, params url.Values) ListUserPoliciesResult {
	t.Helper()

	params.Set(paramUserName, testUserName)
	rec := mustCallIAM(t, svc, actionListUserPolicies, params)

	var resp ListUserPoliciesResponse
	if err := xml.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}

	return resp.ListUserPoliciesResult
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var resp ErrorResponse
	if err := xml.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v body=%s", err, rec.Body.String())
	}

	return resp.Error.Code
}

func TestUserPolicy_PutGetReturnsURLEncodedDocument(t *testing.T) {
	t.Parallel()

	svc := newServiceWithUser(t)
	putUserPolicy(t, svc, "P", testPolicyDoc)

	rec := mustCallIAM(t, svc, actionGetUserPolicy, url.Values{paramUserName: {testUserName}, paramPolicyName: {"P"}})

	var resp GetUserPolicyResponse
	if err := xml.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}

	got := resp.GetUserPolicyResult
	if got.UserName != testUserName || got.PolicyName != "P" {
		t.Errorf("result = %+v", got)
	}

	doc, err := url.QueryUnescape(got.PolicyDocument)
	if err != nil || doc != testPolicyDoc {
		t.Errorf("decoded PolicyDocument = %q (%v), want %q", doc, err, testPolicyDoc)
	}

	if strings.ContainsAny(got.PolicyDocument, `{"+ `) {
		t.Errorf("PolicyDocument = %q, want it URL-encoded", got.PolicyDocument)
	}
}

func TestUserPolicy_PutSameNameOverwrites(t *testing.T) {
	t.Parallel()

	svc := newServiceWithUser(t)
	putUserPolicy(t, svc, "P", `{"Version":"2012-10-17"}`)
	putUserPolicy(t, svc, "P", testPolicyDoc)

	if got := listUserPolicies(t, svc, url.Values{}).PolicyNames; !slices.Equal(got, []string{"P"}) {
		t.Errorf("PolicyNames = %v, want [P]", got)
	}

	rec := mustCallIAM(t, svc, actionGetUserPolicy, url.Values{paramUserName: {testUserName}, paramPolicyName: {"P"}})
	if !strings.Contains(rec.Body.String(), url.QueryEscape("Deny")) {
		t.Errorf("GetUserPolicy = %s, want the second document", rec.Body.String())
	}
}

func TestUserPolicy_ListIsSortedAndPaged(t *testing.T) {
	t.Parallel()

	svc := newServiceWithUser(t)
	for _, name := range []string{"c", "a", "b"} {
		putUserPolicy(t, svc, name, testPolicyDoc)
	}

	all := listUserPolicies(t, svc, url.Values{})
	if !slices.Equal(all.PolicyNames, []string{"a", "b", "c"}) || all.IsTruncated {
		t.Errorf("list = %+v, want [a b c] not truncated", all)
	}

	first := listUserPolicies(t, svc, url.Values{"MaxItems": {"2"}})
	if !slices.Equal(first.PolicyNames, []string{"a", "b"}) || !first.IsTruncated || first.Marker == "" {
		t.Fatalf("first page = %+v, want [a b] truncated with a marker", first)
	}

	second := listUserPolicies(t, svc, url.Values{"MaxItems": {"2"}, "Marker": {first.Marker}})
	if !slices.Equal(second.PolicyNames, []string{"c"}) || second.IsTruncated || second.Marker != "" {
		t.Errorf("second page = %+v, want [c] not truncated", second)
	}
}

func TestUserPolicy_DeleteRemovesPolicyAndUnblocksDeleteUser(t *testing.T) {
	t.Parallel()

	svc := newServiceWithUser(t)
	putUserPolicy(t, svc, "P", testPolicyDoc)

	rec := callIAM(t, svc, "DeleteUser", url.Values{paramUserName: {testUserName}})
	if rec.Code != http.StatusConflict || errorCode(t, rec) != errDeleteConflict {
		t.Fatalf("DeleteUser with an inline policy: code=%d body=%s, want 409 DeleteConflict", rec.Code, rec.Body.String())
	}

	mustCallIAM(t, svc, actionDeleteUserPolicy, url.Values{paramUserName: {testUserName}, paramPolicyName: {"P"}})

	if got := listUserPolicies(t, svc, url.Values{}).PolicyNames; len(got) != 0 {
		t.Errorf("PolicyNames after delete = %v, want none", got)
	}

	mustCallIAM(t, svc, "DeleteUser", url.Values{paramUserName: {testUserName}})
}

//nolint:funlen // Table-driven test covering every error case.
func TestUserPolicy_Errors(t *testing.T) {
	t.Parallel()

	svc := newServiceWithUser(t)
	putUserPolicy(t, svc, "P", testPolicyDoc)

	tests := []struct {
		name       string
		action     string
		params     url.Values
		wantStatus int
		wantCode   string
	}{
		{
			name:       "put for unknown user",
			action:     actionPutUserPolicy,
			params:     url.Values{paramUserName: {testUnknownUser}, paramPolicyName: {"P"}, paramPolicyDocument: {testPolicyDoc}},
			wantStatus: http.StatusNotFound,
			wantCode:   errNoSuchEntity,
		},
		{
			name:       "get for unknown user",
			action:     actionGetUserPolicy,
			params:     url.Values{paramUserName: {testUnknownUser}, paramPolicyName: {"P"}},
			wantStatus: http.StatusNotFound,
			wantCode:   errNoSuchEntity,
		},
		{
			name:       "get unknown policy",
			action:     actionGetUserPolicy,
			params:     url.Values{paramUserName: {testUserName}, paramPolicyName: {"missing"}},
			wantStatus: http.StatusNotFound,
			wantCode:   errNoSuchEntity,
		},
		{
			name:       "list for unknown user",
			action:     actionListUserPolicies,
			params:     url.Values{paramUserName: {testUnknownUser}},
			wantStatus: http.StatusNotFound,
			wantCode:   errNoSuchEntity,
		},
		{
			name:       "delete for unknown user",
			action:     actionDeleteUserPolicy,
			params:     url.Values{paramUserName: {testUnknownUser}, paramPolicyName: {"P"}},
			wantStatus: http.StatusNotFound,
			wantCode:   errNoSuchEntity,
		},
		{
			name:       "delete unknown policy",
			action:     actionDeleteUserPolicy,
			params:     url.Values{paramUserName: {testUserName}, paramPolicyName: {"missing"}},
			wantStatus: http.StatusNotFound,
			wantCode:   errNoSuchEntity,
		},
		{
			name:       "malformed document",
			action:     actionPutUserPolicy,
			params:     url.Values{paramUserName: {testUserName}, paramPolicyName: {"P"}, paramPolicyDocument: {"not json"}},
			wantStatus: http.StatusBadRequest,
			wantCode:   errMalformedPolicy,
		},
		{
			name:       "missing document",
			action:     actionPutUserPolicy,
			params:     url.Values{paramUserName: {testUserName}, paramPolicyName: {"P"}},
			wantStatus: http.StatusBadRequest,
			wantCode:   errInvalidParameter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := callIAM(t, svc, tt.action, tt.params)
			if rec.Code != tt.wantStatus || errorCode(t, rec) != tt.wantCode {
				t.Errorf("code=%d body=%s, want %d %s", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantCode)
			}
		})
	}
}
