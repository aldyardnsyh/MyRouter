package skillimport

import "testing"

func TestInferCapabilityTypes(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want string
	}{
		{"vision", "---\ndescription: screenshot helper\n---\n# Vision\nTake a screenshot of the page. Analyze the image pixels for layout vision.", "vision"},
		{"memory", "---\ndescription: remember prefs\n---\n# Memory\nAlways remember user preferences and recall them later. Never forget stored memory.", "memory"},
		{"retrieval", "---\ndescription: docs search\n---\n# Docs\nSearch the documentation index and retrieve relevant docs for lookup.", "retrieval"},
		{"chat", "---\ndescription: chat buddy\n---\n# Chat\nHold a friendly conversation with the right tone and greeting persona.", "chat"},
		{"prompt", "---\ndescription: general\n---\n# General\nDo the thing carefully and completely.", "prompt"},
	}
	for _, c := range cases {
		cap := InferCapability(c.md)
		if cap.Type != c.want {
			t.Errorf("%s: got type %q want %q", c.name, cap.Type, c.want)
		}
		if cap.Summary == "" {
			t.Errorf("%s: empty summary", c.name)
		}
		if cap.Prompt == "" {
			t.Errorf("%s: empty prompt", c.name)
		}
	}
}
