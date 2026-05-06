package node

import "testing"

func TestDatasetPublicationFileIDSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		fileID string
		want   string
	}{
		{
			name:   "series file id",
			fileID: "celestrak-gp:OMM.fbs:source-sha:part-000001",
			want:   "OMM.fbs",
		},
		{
			name:   "plain dataset schema",
			fileID: "CAT.fbs",
			want:   "CAT.fbs",
		},
		{
			name:   "no schema",
			fileID: "celestrak-provider-update",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := datasetPublicationFileIDSchema(tt.fileID); got != tt.want {
				t.Fatalf("datasetPublicationFileIDSchema(%q) = %q, want %q", tt.fileID, got, tt.want)
			}
		})
	}
}
