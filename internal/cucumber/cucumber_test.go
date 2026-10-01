package cucumber

import "testing"

// A mixed report: one feature, two scenarios — one all-passed, one with a failed
// step and a screenshot embedding — plus a skipped step. Exercises the status
// tally, scenario pass/fail, and embedding counting in one document.
const mixed = `[
  {
    "uri": "features/login.feature",
    "keyword": "Feature",
    "name": "Login",
    "elements": [
      {
        "keyword": "Scenario",
        "name": "valid credentials",
        "type": "scenario",
        "steps": [
          {"keyword": "Given ", "name": "a user", "result": {"status": "passed", "duration": 1000}},
          {"keyword": "When ", "name": "they log in", "result": {"status": "passed", "duration": 2000}}
        ]
      },
      {
        "keyword": "Scenario",
        "name": "bad password",
        "type": "scenario",
        "steps": [
          {"keyword": "Given ", "name": "a user", "result": {"status": "passed"}},
          {
            "keyword": "When ", "name": "they mistype",
            "result": {"status": "failed", "error_message": "boom"},
            "embeddings": [{"mime_type": "image/png", "data": "QUJD"}]
          },
          {"keyword": "Then ", "name": "never reached", "result": {"status": "skipped"}}
        ]
      }
    ]
  }
]`

func TestParseAndSummary(t *testing.T) {
	rep, err := Parse([]byte(mixed))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := rep.Summary()
	if s.Features != 1 {
		t.Errorf("features = %d, want 1", s.Features)
	}
	if s.Scenarios != 2 || s.ScenariosPassed != 1 || s.ScenariosFailed != 1 {
		t.Errorf("scenarios = %d (%d passed, %d failed), want 2 (1 passed, 1 failed)", s.Scenarios, s.ScenariosPassed, s.ScenariosFailed)
	}
	if s.Steps != 5 {
		t.Errorf("steps = %d, want 5", s.Steps)
	}
	if s.StepStatus["passed"] != 3 || s.StepStatus["failed"] != 1 || s.StepStatus["skipped"] != 1 {
		t.Errorf("step status = %v, want passed 3 / failed 1 / skipped 1", s.StepStatus)
	}
	if s.Embeddings != 1 {
		t.Errorf("embeddings = %d, want 1", s.Embeddings)
	}
}

// A report with a failed step is not Passed; an all-green one is.
func TestPassed(t *testing.T) {
	rep, _ := Parse([]byte(mixed))
	if rep.Passed() {
		t.Error("mixed report with a failed step reported Passed")
	}

	green := `[{"uri":"f","keyword":"Feature","name":"F","elements":[
	  {"keyword":"Scenario","name":"s","type":"scenario","steps":[
	    {"keyword":"Given ","name":"x","result":{"status":"passed"}},
	    {"keyword":"And ","name":"y","result":{"status":"skipped"}}
	  ]}]}]`
	rg, err := Parse([]byte(green))
	if err != nil {
		t.Fatalf("parse green: %v", err)
	}
	if !rg.Passed() {
		t.Error("all passed/skipped report did not report Passed")
	}
}

// Backgrounds are counted as steps but never as scenarios.
func TestBackgroundNotCountedAsScenario(t *testing.T) {
	body := `[{"uri":"f","keyword":"Feature","name":"F","elements":[
	  {"keyword":"Background","name":"bg","type":"background","steps":[
	    {"keyword":"Given ","name":"shared","result":{"status":"passed"}}]},
	  {"keyword":"Scenario","name":"s","type":"scenario","steps":[
	    {"keyword":"When ","name":"act","result":{"status":"passed"}}]}
	]}]`
	rep, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := rep.Summary()
	if s.Scenarios != 1 {
		t.Errorf("scenarios = %d, want 1 (background excluded)", s.Scenarios)
	}
	if s.Steps != 2 {
		t.Errorf("steps = %d, want 2 (background step counted)", s.Steps)
	}
}

// An empty array is a valid report (a run that exercised no features) and is
// vacuously green; non-array JSON and empty input are rejected as not-cucumber-JSON.
func TestParseEdgeCases(t *testing.T) {
	empty, err := Parse([]byte(`[]`))
	if err != nil {
		t.Fatalf("empty array should parse: %v", err)
	}
	if !empty.Passed() || empty.Summary().Features != 0 {
		t.Error("empty report should be vacuously green with zero features")
	}

	if _, err := Parse([]byte(`{"not":"an array"}`)); err == nil {
		t.Error("a JSON object is not cucumber-JSON and must be rejected")
	}
	if _, err := Parse([]byte("   ")); err == nil {
		t.Error("empty input must be rejected")
	}
}

// Unknown fields are tolerated — the point of consuming a shared format rather than
// a specific tool's exact struct.
func TestLenientUnknownFields(t *testing.T) {
	body := `[{"uri":"f","keyword":"Feature","name":"F","unknown_feature_key":42,"elements":[
	  {"keyword":"Scenario","name":"s","type":"scenario","custom":"x","steps":[
	    {"keyword":"Given ","name":"x","result":{"status":"passed","extra":true}}]}]}]`
	rep, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("unknown fields should be tolerated: %v", err)
	}
	if !rep.Passed() {
		t.Error("report with unknown fields should still parse green")
	}
}

// The one-line String rendering is stable and reads naturally.
func TestSummaryString(t *testing.T) {
	rep, _ := Parse([]byte(mixed))
	got := rep.Summary().String()
	want := "1 feature, 2 scenarios (1 passed, 1 failed), 5 steps [failed 1, passed 3, skipped 1], 1 embedding"
	if got != want {
		t.Errorf("summary string:\n got %q\nwant %q", got, want)
	}
}
