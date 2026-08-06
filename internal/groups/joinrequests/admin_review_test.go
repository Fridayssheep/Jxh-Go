package joinrequests

import (
	"errors"
	"testing"
)

func TestParseAdmissionRosterCSV(t *testing.T) {
	entries, err := parseAdmissionRoster("roster.csv", []byte("学号,姓名,专业\n302026315326,测试同学,计算机类\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].StudentID != "302026315326" || entries[0].Major != "计算机类" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if _, err := parseAdmissionRoster("roster.csv", []byte("学号,专业\n302026315326,计算机类\n302026315326,计算机类\n")); err == nil {
		t.Fatal("expected duplicate student ID to fail")
	} else {
		var validation *AdmissionRosterValidationError
		if !errors.As(err, &validation) || validation.Row != 3 || validation.Field != "student_id" {
			t.Fatalf("unexpected duplicate report: %#v", err)
		}
	}
	if _, err := parseAdmissionRoster("roster.csv", []byte("学号,专业\n30202631A326,计算机类\n")); err == nil {
		t.Fatal("expected nonnumeric student ID to fail")
	}
}
