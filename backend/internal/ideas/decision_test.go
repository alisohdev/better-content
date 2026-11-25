package ideas

import (
	"strings"
	"testing"
)

func TestCalculateDecisionUpdateNormalizesReasonAndClearsItAfterRejection(t *testing.T) {
	reason := "  Not a fit  "
	rejected, err := calculateDecisionUpdate("NEW", nil, "REJECTED", &reason)
	if err != nil || rejected.noop || rejected.status != "REJECTED" || rejected.rejectionReason == nil || *rejected.rejectionReason != "Not a fit" {
		t.Fatalf("rejection update = %+v, err=%v", rejected, err)
	}
	sameReason := "Not a fit"
	noop, err := calculateDecisionUpdate("REJECTED", rejected.rejectionReason, "REJECTED", &sameReason)
	if err != nil || !noop.noop {
		t.Fatalf("same rejection update = %+v, err=%v", noop, err)
	}
	accepted, err := calculateDecisionUpdate("REJECTED", rejected.rejectionReason, "ACCEPTED", &reason)
	if err != nil || accepted.noop || accepted.status != "ACCEPTED" || accepted.rejectionReason != nil {
		t.Fatalf("acceptance update = %+v, err=%v", accepted, err)
	}
	if _, err := calculateDecisionUpdate("NEW", nil, "REJECTED", stringPointer(strings.Repeat("x", 501))); err == nil {
		t.Fatal("accepted a rejection reason longer than the database limit")
	}
}

func stringPointer(value string) *string { return &value }
