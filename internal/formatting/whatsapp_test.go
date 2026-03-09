package formatting

import "testing"

func TestWhatsApp(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{name: "empty", input: "", want: ""},
		{name: "plain text", input: "Hello world", want: "Hello world"},
		{name: "double bold", input: "This is **important**", want: "This is *important*"},
		{name: "underscore bold", input: "This is __important__", want: "This is *important*"},
		{name: "strikethrough", input: "This is ~~wrong~~", want: "This is ~wrong~"},
		{name: "link same label", input: "[https://example.com](https://example.com)", want: "https://example.com"},
		{name: "link diff label", input: "[Maps](https://maps.google.com)", want: "Maps\nhttps://maps.google.com"},
		{name: "header to bold", input: "# Title", want: "*Title*"},
		{name: "hr removed", input: "A\n* * *\nB", want: "A\n\nB"},
		{name: "asterisk list to dash", input: "* item one\n* item two", want: "- item one\n- item two"},
		{name: "asterisk list extra spaces normalized", input: "*   item one\n*     item two", want: "- item one\n- item two"},
		{name: "code fence lang stripped", input: "```python\nprint(1)\n```", want: "```\nprint(1)\n```"},
		{name: "blockquotes kept", input: "> quote", want: "> quote"},
		{
			name: "real bug pattern bullet bold and markdown link",
			input: "*   **Image Description:** The image shows a cyclist.\n" +
				"*   **Google Maps Link:** [https://www.google.com/maps?q=18.940728,-103.895411](https://www.google.com/maps?q=18.940728,-103.895411)",
			want: "- *Image Description:* The image shows a cyclist.\n" +
				"- *Google Maps Link:* https://www.google.com/maps?q=18.940728,-103.895411",
		},
		{
			name:  "user reported link in sentence",
			input: "*Location:* This is a Google Maps location [https://www.google.com/maps?q=18.94,-103.89](https://www.google.com/maps?q=18.94,-103.89).",
			want:  "*Location:* This is a Google Maps location https://www.google.com/maps?q=18.94,-103.89.",
		},
		{
			name: "full LLM output",
			input: "* * *\n\n## **Analysis**\n\n*   *GPS:* `18.94° N`\n*   *Camera:* Pixel 7\n\n" +
				"Maps: [https://www.google.com/maps?q=18.94,-103.89](https://www.google.com/maps?q=18.94,-103.89)\n\n* * *",
			want: "*Analysis*\n\n- *GPS:* `18.94° N`\n- *Camera:* Pixel 7\n\n" +
				"Maps: https://www.google.com/maps?q=18.94,-103.89",
		},
		{
			name:  "literal bold and italic stripped adjacent to asterisks",
			input: "bold*Current Weather in Langley, BC:*italic",
			want:  "*Current Weather in Langley, BC:*",
		},
		{
			name:  "prose with bold/italic unchanged",
			input: "Pick the bold choice. Use italic font for emphasis.",
			want:  "Pick the bold choice. Use italic font for emphasis.",
		},
		{
			name:  "unclosed bold at line start gets closing asterisk",
			input: "*Current Weather in White Rock, BC:  \n🌡️ Temperature: 10.8°C\n*Today's Forecast:  \n- Day: Cloudy",
			want:  "*Current Weather in White Rock, BC:  *\n🌡️ Temperature: 10.8°C\n*Today's Forecast:  *\n- Day: Cloudy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WhatsApp(tt.input)
			if got != tt.want {
				t.Errorf("WhatsApp(%q)\n  got:  %q\n  want: %q", tt.input, got, tt.want)
			}
		})
	}
}
