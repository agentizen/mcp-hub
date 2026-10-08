package main

import (
	"testing"
	"time"
)

func TestMaxResponseBodyBytes_MatchesRequestCap(t *testing.T) {
	if MaxResponseBodyBytes != 10<<20 {
		t.Errorf("MaxResponseBodyBytes = %d, want %d", MaxResponseBodyBytes, 10<<20)
	}
	if MaxResponseBodyBytes != MaxRequestBodyBytes {
		t.Errorf("response cap %d != request cap %d", MaxResponseBodyBytes, MaxRequestBodyBytes)
	}
}

func TestRequestForwardTimeout_IsFiveMinutes(t *testing.T) {
	if RequestForwardTimeout != 5*time.Minute {
		t.Errorf("RequestForwardTimeout = %v, want 5 min", RequestForwardTimeout)
	}
}
