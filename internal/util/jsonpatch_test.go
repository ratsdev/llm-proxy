package util

import "testing"

func TestReplaceJSONStringField(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		input  string
		key    string
		value  string
		want   string
		change bool
	}{
		{
			name:   "top level",
			input:  `{"model":"upstream","n":1}`,
			key:    "model",
			value:  "public",
			want:   `{"model":"public","n":1}`,
			change: true,
		},
		{
			name:   "every occurrence",
			input:  `{"model":"a","response":{"model":"a"}}`,
			key:    "model",
			value:  "b",
			want:   `{"model":"b","response":{"model":"b"}}`,
			change: true,
		},
		{
			name:   "key text inside a string is untouched",
			input:  `{"model":"a","text":"say \"model\":\"x\""}`,
			key:    "model",
			value:  "b",
			want:   `{"model":"b","text":"say \"model\":\"x\""}`,
			change: true,
		},
		{
			name:   "already current",
			input:  `{"model":"public"}`,
			key:    "model",
			value:  "public",
			want:   "",
			change: false,
		},
		{
			name:   "non string value is left alone",
			input:  `{"model":42}`,
			key:    "model",
			value:  "public",
			want:   "",
			change: false,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, changed := ReplaceJSONStringField([]byte(test.input), test.key, test.value)
			if changed != test.change {
				t.Fatalf("changed = %v, want %v", changed, test.change)
			}
			if string(got) != test.want {
				t.Fatalf("result = %s, want %s", got, test.want)
			}
		})
	}
}
