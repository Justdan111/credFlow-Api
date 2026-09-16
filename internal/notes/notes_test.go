package notes

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateCreate(t *testing.T) {
	tests := []struct {
		name        string
		req         CreateRequest
		wantBody    string
		wantChannel string
		wantErr     bool
	}{
		{
			name:        "defaults the channel",
			req:         CreateRequest{Body: "Called, promised Friday"},
			wantBody:    "Called, promised Friday",
			wantChannel: "note",
		},
		{
			name:        "keeps an explicit channel",
			req:         CreateRequest{Body: "Rang twice", Channel: "call"},
			wantBody:    "Rang twice",
			wantChannel: "call",
		},
		{
			name:        "trims the body",
			req:         CreateRequest{Body: "  spaced out \n", Channel: "visit"},
			wantBody:    "spaced out",
			wantChannel: "visit",
		},
		{
			name:    "rejects an empty body",
			req:     CreateRequest{Body: "   "},
			wantErr: true,
		},
		{
			// Mirrors the CHECK in migration 0011, so this is a 400 rather than
			// a 500 from a constraint violation.
			name:    "rejects a whitespace-only body",
			req:     CreateRequest{Body: "\t\n "},
			wantErr: true,
		},
		{
			name:    "rejects an over-long body",
			req:     CreateRequest{Body: strings.Repeat("a", maxBodyLength+1)},
			wantErr: true,
		},
		{
			name:    "rejects an unknown channel",
			req:     CreateRequest{Body: "hello", Channel: "carrier-pigeon"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, channel, err := validateCreate(tc.req)

			if tc.wantErr {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("got %v, want ErrValidation", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if body != tc.wantBody {
				t.Errorf("body: got %q, want %q", body, tc.wantBody)
			}
			if channel != tc.wantChannel {
				t.Errorf("channel: got %q, want %q", channel, tc.wantChannel)
			}
		})
	}
}

func TestChannels_matchTheDatabaseConstraint(t *testing.T) {
	// Kept in step with the CHECK in migration 0011 by hand, so a mismatch here
	// would mean the API accepts a value the database rejects.
	want := []string{"note", "call", "sms", "email", "visit"}
	if len(channels) != len(want) {
		t.Fatalf("channel count: got %d, want %d", len(channels), len(want))
	}
	for _, c := range want {
		if _, ok := channels[c]; !ok {
			t.Errorf("channel %q is missing", c)
		}
	}
}
