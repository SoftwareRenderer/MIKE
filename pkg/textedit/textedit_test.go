package textedit

import "testing"

// Every refusal Apply answers with is the model's instruction for the next attempt, so each shape it
// can get wrong is refused by its full sentence and nothing is returned as a result.
func TestApplyRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		edits   []Edit
		want    string
		wantErr string
	}{
		{
			name: "one edit", content: "hello world\nfoo bar\nbaz qux",
			edits: []Edit{{OldText: "hello world", NewText: "goodbye world"}},
			want:  "goodbye world\nfoo bar\nbaz qux",
		},
		{
			name: "several edits are applied in content order, not call order", content: "alpha\nbeta\ngamma\ndelta",
			edits: []Edit{{OldText: "gamma", NewText: "GAMMA"}, {OldText: "alpha", NewText: "ALPHA"}},
			want:  "ALPHA\nbeta\nGAMMA\ndelta",
		},
		{
			name: "edits that meet end to end are not an overlap", content: "abcdefgh",
			edits: []Edit{{OldText: "abcd", NewText: "ABCD"}, {OldText: "efgh", NewText: "EFGH"}},
			want:  "ABCDEFGH",
		},
		{
			name: "empty oldText", content: "content",
			edits:   []Edit{{OldText: "", NewText: "x"}},
			wantErr: "edits[0].oldText is empty",
		},
		{
			name: "text the file does not hold", content: "hello world",
			edits:   []Edit{{OldText: "not here", NewText: "x"}},
			wantErr: "edits[0].oldText not found in file. Ensure it matches exactly (whitespace, indentation). Got 1 lines in file",
		},
		{
			name: "text the file holds twice", content: "foo\nbar\nfoo\nbaz",
			edits:   []Edit{{OldText: "foo", NewText: "BAR"}},
			wantErr: "edits[0].oldText appears multiple times in file. Make it more specific by including surrounding context",
		},
		{
			name: "two edits over the same text name both, as the model wrote them", content: "abcdefgh",
			edits:   []Edit{{OldText: "cdef", NewText: "CDEF"}, {OldText: "abcd", NewText: "ABCD"}},
			wantErr: "edits[1] and edits[0] overlap. Merge them into a single edit or ensure they don't overlap",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Apply(tc.content, tc.edits)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Apply accepted a call it must refuse; got %q", got)
				}
				if err.Error() != tc.wantErr {
					t.Errorf("refusal = %q, want %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got != tc.want {
				t.Errorf("result = %q, want %q", got, tc.want)
			}
		})
	}
}
