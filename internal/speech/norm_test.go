package speech

import "testing"

func TestNormalizeForSpeech(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The two whisper-verified defect strings (2026-08-06).
		{"defect digits list",
			"1, 2, 3, 4, 5, 6, 7, 8, 9, 10.",
			"one, two, three, four, five, six, seven, eight, nine, ten."},
		{"defect hundreds list",
			"100, 200, 300, 400, 500, 600, 700, 800, 900, 1000.",
			"one hundred, two hundred, three hundred, four hundred, five hundred, six hundred, seven hundred, eight hundred, nine hundred, one thousand."},

		// Plain integers, years as plain cardinals.
		{"zero", "0", "zero"},
		{"teens", "It took 17 tries.", "It took seventeen tries."},
		{"round tens", "40", "forty"},
		{"year is plain cardinal", "Shipped in 2026.", "Shipped in two thousand twenty six."},
		{"hundreds with tens and ones", "123", "one hundred twenty three"},
		{"bare thousand no comma", "1000", "one thousand"},
		{"leading zeros digit-by-digit", "agent 007", "agent zero zero seven"},

		// Thousands-comma rule: comma groups iff leading group 1-3 digits and
		// every comma is followed by exactly three digits.
		{"thousands comma", "1,000", "one thousand"},
		{"millions comma", "1,000,000", "one million"},
		{"grouped with remainder text", "2,345 items", "two thousand three hundred forty five items"},
		{"int32 max", "2,147,483,647",
			"two billion one hundred forty seven million four hundred eighty three thousand six hundred forty seven"},
		{"top of scope", "999,999,999,999",
			"nine hundred ninety nine billion nine hundred ninety nine million nine hundred ninety nine thousand nine hundred ninety nine"},
		{"comma with space is a list", "1, 2", "one, two"},
		{"comma with two digits is a list", "1,23", "one,twenty three"},
		{"leading group too long is a list", "1234,567",
			"one thousand two hundred thirty four,five hundred sixty seven"},
		{"group followed by 4th digit is a list", "1,2345",
			"one,two thousand three hundred forty five"},

		// Beyond the billions scope: digit-by-digit.
		{"thirteen digits digit-by-digit", "1234567890123",
			"one two three four five six seven eight nine zero one two three"},

		// Minus (verbatim toSpoken port: only after start/whitespace).
		{"minus integer", "-10", "minus ten"},
		{"minus in sentence", "It was -10 degrees.", "It was minus ten degrees."},
		{"minus decimal", "-3.5", "minus three point five"},
		{"hyphenated word untouched", "Fine-Tune", "Fine-Tune"},
		{"range hyphen is not minus", "5-10", "five-ten"},
		{"paren hyphen is not minus", "(-5)", "(-five)"},

		// Decimals.
		{"pi", "Pi is 3.14.", "Pi is three point one four."},
		{"zero point five", "0.5", "zero point five"},
		{"long fraction digit-by-digit", "2.718281", "two point seven one eight two eight one"},

		// Percent.
		{"percent", "Progress hit 50%.", "Progress hit fifty percent."},
		{"decimal percent", "3.5%", "three point five percent"},

		// Versions (2+ dots): digit-by-digit with "point".
		{"semver", "1.2.3", "one point two point three"},
		{"semver in sentence", "Version 1.2.3 shipped.", "Version one point two point three shipped."},
		{"multi-digit version components", "10.15.7", "one zero point one five point seven"},
		{"four part version", "1.2.3.4", "one point two point three point four"},
		{"version at sentence end", "Use 1.2.3.", "Use one point two point three."},

		// Untouched: word-glued digits, ordinals (engine-verified correct),
		// hex, clock times (engine-verified correct).
		{"trailing digits in word", "abc123", "abc123"},
		{"leading digits before word", "123abc", "123abc"},
		{"filename", "file2.txt", "file2.txt"},
		{"version with letter component", "1.2.3b4", "1.2.3b4"},
		{"ordinal 2nd untouched", "This is the 2nd attempt.", "This is the 2nd attempt."},
		{"ordinal 21st untouched", "the 21st of March", "the 21st of March"},
		{"ordinal 103rd untouched", "for the 103rd time", "for the 103rd time"},
		{"hex lower", "0x1F", "0x1F"},
		{"hex word", "0xdead", "0xdead"},
		{"time", "It is 11:41.", "It is 11:41."},
		{"time with oh minutes", "Meet at 9:05.", "Meet at 9:05."},
		{"time on the hour", "12:00", "12:00"},
		{"timestamp with seconds", "11:41:07", "11:41:07"},
		{"invalid hour is not a time", "24:00", "twenty four:zero zero"},
		{"ratio is not a time", "3:1", "three:one"},

		// No numbers: byte-for-byte identical.
		{"plain prose", "Hello! This is jane speaking.", "Hello! This is jane speaking."},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := NormalizeForSpeech(c.in); got != c.want {
			t.Errorf("%s: NormalizeForSpeech(%q)\n got  %q\n want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestNormalizeBeforeSplit pins the interaction that motivated normalizing
// before SplitSentences: dots inside numbers must not become sentence
// boundaries (pre-fix, "Pi is 3.14." split into "Pi is 3." + "14.", and
// "1.2.3" became three engine calls — the whisper-verified stutter).
func TestNormalizeBeforeSplit(t *testing.T) {
	got := SplitSentences(NormalizeForSpeech("Pi is 3.14. Version 1.2.3 shipped."))
	want := []string{"Pi is three point one four.", "Version one point two point three shipped."}
	if len(got) != len(want) {
		t.Fatalf("got %d sentences %q, want %q", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence %d = %q, want %q", i, got[i], want[i])
		}
	}
}
