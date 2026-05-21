package loop

import (
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
)

func TestMapVolumes(t *testing.T) {
	tests := []struct {
		name   string
		in     []client.Volume
		repoID int64
		want   []orchestrator.Volume
	}{
		{
			name: "nil input",
			in:   nil,
			want: nil,
		},
		{
			name: "empty slice",
			in:   []client.Volume{},
			want: nil,
		},
		{
			name: "single volume no repo prefix (repoID=0)",
			in: []client.Volume{
				{Name: "npm-cache", Mount: "/root/.npm"},
			},
			repoID: 0,
			want: []orchestrator.Volume{
				{Name: "npm-cache", Mount: "/root/.npm"},
			},
		},
		{
			name: "single volume with repo prefix",
			in: []client.Volume{
				{Name: "npm-cache", Mount: "/root/.npm"},
			},
			repoID: 6,
			want: []orchestrator.Volume{
				{Name: "repo-6-npm-cache", Mount: "/root/.npm"},
			},
		},
		{
			name: "multiple volumes preserve order with repo prefix",
			in: []client.Volume{
				{Name: "go-cache", Mount: "/root/.cache/go-build"},
				{Name: "mod-cache", Mount: "/go/pkg/mod"},
				{Name: "tmp-cache", Mount: "/tmp/build"},
			},
			repoID: 6,
			want: []orchestrator.Volume{
				{Name: "repo-6-go-cache", Mount: "/root/.cache/go-build"},
				{Name: "repo-6-mod-cache", Mount: "/go/pkg/mod"},
				{Name: "repo-6-tmp-cache", Mount: "/tmp/build"},
			},
		},
		{
			name: "multiple volumes no prefix (repoID=0)",
			in: []client.Volume{
				{Name: "go-cache", Mount: "/root/.cache/go-build"},
				{Name: "mod-cache", Mount: "/go/pkg/mod"},
				{Name: "tmp-cache", Mount: "/tmp/build"},
			},
			repoID: 0,
			want: []orchestrator.Volume{
				{Name: "go-cache", Mount: "/root/.cache/go-build"},
				{Name: "mod-cache", Mount: "/go/pkg/mod"},
				{Name: "tmp-cache", Mount: "/tmp/build"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapVolumes(tt.in, tt.repoID)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
