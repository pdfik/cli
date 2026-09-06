package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfik/cli/internal/apitest"
)

const testInvoiceXML = "<rsm:CrossIndustryInvoice/>"

func writeInvoice(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "invoice.xml")
	if err := os.WriteFile(p, []byte(testInvoiceXML), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEInvoiceSubmitsXMLAndOptions(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	in := writeInvoice(t, dir)
	r := execute(t, f, nil, "einvoice-to-pdf", in, "--profile", "en16931",
		"--template", "3e1c1e5e-0000-4000-8000-000000000001",
		"--webhook", "https://example.com/hooks/pdf", "-o", dir, "-f", "invoice.pdf")
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "invoice.pdf")); err != nil {
		t.Fatalf("output not written: %v", err)
	}
	body := f.LastBody(t)
	if body["xml"] != testInvoiceXML {
		t.Fatalf("xml body wrong: %q", body["xml"])
	}
	if body["profile"] != "en16931" || body["template_id"] != "3e1c1e5e-0000-4000-8000-000000000001" ||
		body["webhook_url"] != "https://example.com/hooks/pdf" {
		t.Fatalf("unexpected body: %v", body)
	}
	if _, ok := body["test"]; ok {
		t.Fatalf("test must not be sent unless set: %v", body)
	}
}

func TestEInvoiceDefaultsStayTheAPIsBusiness(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, nil, "einvoice-to-pdf", writeInvoice(t, dir), "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	body := f.LastBody(t)
	for _, key := range []string{"profile", "template_id", "webhook_url", "test"} {
		if _, ok := body[key]; ok {
			t.Fatalf("%s must not be sent unless set: %v", key, body)
		}
	}
}

func TestEInvoiceReadsStdinAndSendsTest(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, strings.NewReader(testInvoiceXML), "einvoice-to-pdf", "-", "--test", "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	body := f.LastBody(t)
	if body["xml"] != testInvoiceXML || body["test"] != true {
		t.Fatalf("unexpected body: %v", body)
	}
	mustContain(t, r.stderr, "test mode")
}

func TestEInvoiceClientSideValidationIsAUsageError(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown profile", []string{"einvoice-to-pdf", "invoice.xml", "--profile", "en16931-extended"}, "extended"},
		{"upper-case profile", []string{"einvoice-to-pdf", "invoice.xml", "--profile", "EN16931"}, "not supported"},
		{"two positionals", []string{"einvoice-to-pdf", "a.xml", "b.xml"}, "exactly one input"},
		{"no positional", []string{"einvoice-to-pdf"}, "exactly one input"},
		{"page flag refused", []string{"einvoice-to-pdf", "invoice.xml", "--format", "A4"}, "format"},
		{"missing file", []string{"einvoice-to-pdf", "missing.xml", "--api-key", "k"}, "missing.xml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := execute(t, nil, nil, tc.args...)
			if r.code != exitUsage {
				t.Fatalf("exit %d, want %d: %s", r.code, exitUsage, r.stderr)
			}
			mustContain(t, r.stderr, tc.want)
		})
	}
}

func TestEInvoiceNonInvoiceProfileNote(t *testing.T) {
	f := apitest.New(t)
	dir := t.TempDir()
	r := execute(t, f, nil, "einvoice-to-pdf", writeInvoice(t, dir), "--profile", "basicwl", "-o", dir)
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	mustContain(t, r.stderr, "NOT a legally sufficient e-invoice")
	if f.LastBody(t)["profile"] != "basicwl" {
		t.Fatalf("profile not sent: %v", f.LastBody(t))
	}

	r = execute(t, f, nil, "einvoice-to-pdf", writeInvoice(t, dir), "--profile", "minimum", "-q", "-o", dir)
	if r.code != exitOK || r.stderr != "" {
		t.Fatalf("-q must silence the note: %d %q", r.code, r.stderr)
	}
}
