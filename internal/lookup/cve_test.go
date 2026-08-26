package lookup

import (
	"context"
	"strings"
	"testing"
)

const xzRecord = `{"vulnerabilities":[{"cve":{
  "id":"CVE-2024-3094","published":"2024-03-29T17:15:21.150","vulnStatus":"Modified",
  "descriptions":[{"lang":"es","value":"texto"},{"lang":"en","value":"Malicious code was discovered in the upstream tarballs of xz."}],
  "metrics":{"cvssMetricV31":[{"cvssData":{"baseScore":10.0,"baseSeverity":"CRITICAL"}}]}}}]}`

func TestCVERecordParsing(t *testing.T) {
	withFixtureBody(t, "https://services.nvd.nist.gov/rest/json/cves/2.0?cveId=CVE-2024-3094", xzRecord, func() {
		record, found, failed := fetchCVE(context.Background(), "CVE-2024-3094")
		if !found || failed {
			t.Fatalf("found=%v failed=%v", found, failed)
		}
		if record.severity != "CRITICAL" || record.score != "10" {
			t.Errorf("severity=%q score=%q", record.severity, record.score)
		}
		if record.published != "2024-03-29" {
			t.Errorf("published = %q, want the date without the time", record.published)
		}
		if !strings.HasPrefix(record.description, "Malicious code") {
			t.Errorf("description = %q, want the English one", record.description)
		}
	})
}

// An identifier the database does not carry is not an outage.
func TestCVEUnknownIsNotAnOutage(t *testing.T) {
	withFixtureBody(t, "https://services.nvd.nist.gov/rest/json/cves/2.0?cveId=CVE-2099-9999",
		`{"vulnerabilities":[]}`, func() {
			_, found, failed := fetchCVE(context.Background(), "CVE-2099-9999")
			if found {
				t.Error("an empty result means the record does not exist")
			}
			if failed {
				t.Error("an empty result is an answer, not an outage")
			}
		})
}

// Only a published identifier reaches the network.
func TestCVEIdentifierShape(t *testing.T) {
	for _, id := range []string{"CVE-2024-3094", "cve-1999-0001", "CVE-2024-1234567890"} {
		if !cveIDRe.MatchString(id) {
			t.Errorf("%q is a valid identifier", id)
		}
	}
	for _, id := range []string{"", "CVE-24-1", "GHSA-xxxx", "CVE-2024-3094 OR 1=1", "../x", "CVE-2024-123"} {
		if cveIDRe.MatchString(id) {
			t.Errorf("%q must not reach the network", id)
		}
	}
}

// An advisory longer than a chat message is cut on a rune boundary, not mid-character.
func TestCVEDescriptionCut(t *testing.T) {
	long := strings.Repeat("内核", 500)
	cut := capRunes(long, 10)
	if len([]rune(cut)) > 11 {
		t.Errorf("cut = %d runes, want at most 11 including the ellipsis", len([]rune(cut)))
	}
	if !strings.HasSuffix(cut, "…") {
		t.Error("a cut description must show that it was cut")
	}
	if strings.ContainsRune(cut, '�') {
		t.Error("the cut split a character")
	}
}
