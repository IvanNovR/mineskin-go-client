package mineskin

import (
	"encoding/json"
	"testing"
	"time"
)

func TestJobStatusPredicates(t *testing.T) {
	tests := []struct {
		status  JobStatus
		pending bool
		done    bool
	}{
		{JobStatusWaiting, true, false},
		{JobStatusProcessing, true, false},
		{JobStatusCompleted, false, true},
		{JobStatusFailed, false, true},
		{JobStatusUnknown, false, false},
		{JobStatus("weird"), false, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsPending(); got != tt.pending {
				t.Errorf("IsPending() = %v, want %v", got, tt.pending)
			}
			if got := tt.status.IsDone(); got != tt.done {
				t.Errorf("IsDone() = %v, want %v", got, tt.done)
			}
		})
	}
}

func TestJobUnmarshalJSON(t *testing.T) {
	wantResult := UUID{0x01, 0x23, 0xab, 0xcd, 0x45, 0x67, 0x89, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	const dashed = "0123abcd-4567-89ef-0123-456789abcdef"
	const undashed = "0123abcd456789ef0123456789abcdef"

	tests := []struct {
		name    string
		in      string
		want    Job
		wantErr bool
	}{
		{
			name: "full job",
			in:   `{"id":"job-1","status":"completed","timestamp":1700000000000,"eta":1700000005000,"result":"` + dashed + `"}`,
			want: Job{ID: "job-1", Status: JobStatusCompleted, Timestamp: time.UnixMilli(1700000000000),
				ETA: time.UnixMilli(1700000005000), Result: wantResult},
		},
		{name: "timestamp and eta absent", in: `{"id":"a","status":"waiting"}`, want: Job{ID: "a", Status: JobStatusWaiting}},
		{name: "timestamp and eta zero", in: `{"id":"a","status":"waiting","timestamp":0,"eta":0}`, want: Job{ID: "a", Status: JobStatusWaiting}},
		{name: "timestamp and eta null", in: `{"id":"a","status":"waiting","timestamp":null,"eta":null}`, want: Job{ID: "a", Status: JobStatusWaiting}},
		{name: "result absent", in: `{"id":"a","status":"processing"}`, want: Job{ID: "a", Status: JobStatusProcessing}},
		{name: "result null", in: `{"id":"a","status":"processing","result":null}`, want: Job{ID: "a", Status: JobStatusProcessing}},
		{name: "result empty string", in: `{"id":"a","status":"processing","result":""}`, want: Job{ID: "a", Status: JobStatusProcessing}},
		{name: "undashed result", in: `{"id":"a","status":"completed","result":"` + undashed + `"}`, want: Job{ID: "a", Status: JobStatusCompleted, Result: wantResult}},
		{name: "malformed result", in: `{"id":"a","status":"completed","result":"not-a-uuid"}`, wantErr: true},
		{name: "non-string result", in: `{"id":"a","status":"completed","result":123}`, wantErr: true},
		{name: "unknown status kept", in: `{"id":"a","status":"weird"}`, want: Job{ID: "a", Status: JobStatus("weird")}},
		{name: "non-string id", in: `{"id":123,"status":"waiting"}`, wantErr: true},
		{name: "non-integer timestamp", in: `{"id":"a","timestamp":"x"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Job
			err := json.Unmarshal([]byte(tt.in), &got)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got job %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ID != tt.want.ID || got.Status != tt.want.Status || got.Result != tt.want.Result ||
				!got.Timestamp.Equal(tt.want.Timestamp) || !got.ETA.Equal(tt.want.ETA) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestIsPollDone(t *testing.T) {
	result := UUID{1}
	tests := []struct {
		name string
		job  Job
		want bool
	}{
		{"failed", Job{Status: JobStatusFailed}, true},
		{"completed with result", Job{Status: JobStatusCompleted, Result: result}, true},
		{"completed without result", Job{Status: JobStatusCompleted}, false},
		{"unknown", Job{Status: JobStatus("weird"), Result: result}, false},
		{"waiting", Job{Status: JobStatusWaiting}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPollDone(tt.job); got != tt.want {
				t.Errorf("isPollDone() = %v, want %v", got, tt.want)
			}
		})
	}
}
