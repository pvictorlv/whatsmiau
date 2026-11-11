package whatsmiau

import "testing"

type sentChunk struct {
	size int
	last bool
}

func chunkRun(t *testing.T, size, total int) []sentChunk {
	t.Helper()
	var sent []sentChunk
	c := newHistoryChunker(size, func(messages []WookMessageData, last bool) {
		sent = append(sent, sentChunk{len(messages), last})
	})
	for i := 0; i < total; i++ {
		c.add(WookMessageData{})
	}
	c.finish()
	return sent
}

func TestHistoryChunkerMarksOnlyTheFinalChunkLast(t *testing.T) {
	cases := []struct {
		name        string
		size, total int
		want        []sentChunk
	}{
		{"empty blob sends nothing", 3, 0, nil},
		{"fewer than a chunk", 3, 2, []sentChunk{{2, true}}},
		{"exactly one chunk", 3, 3, []sentChunk{{3, true}}},
		{"divides evenly", 3, 6, []sentChunk{{3, false}, {3, true}}},
		{"remainder", 3, 7, []sentChunk{{3, false}, {3, false}, {1, true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chunkRun(t, tc.size, tc.total)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("chunk %d: got %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestHistoryChunkerKeepsEveryMessage(t *testing.T) {
	total := 0
	for _, c := range chunkRun(t, 250, 1001) {
		total += c.size
	}
	if total != 1001 {
		t.Fatalf("delivered %d messages, want 1001", total)
	}
}
