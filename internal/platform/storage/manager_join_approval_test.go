package storage

import "testing"

func TestAggregateMajorCountsMergesOnlyNormalizedVariants(t *testing.T) {
	counts := aggregateMajorCounts([]majorEvidenceCountRow{
		{NormalizedMajor: "计算机类", Major: "计算机类", Count: 2},
		{NormalizedMajor: "计算机类", Major: "计算机类卓越班", Count: 1},
		{NormalizedMajor: "计算机科学与技术", Major: "计算机科学与技术", Count: 3},
	})
	if len(counts) != 2 {
		t.Fatalf("expected two AI evidence groups, got %+v", counts)
	}
	if counts[0].Major != "计算机科学与技术" || counts[0].Count != 3 {
		t.Fatalf("expected semantic variant to remain independent, got %+v", counts)
	}
	if counts[1].Major != "计算机类" || counts[1].Count != 3 {
		t.Fatalf("expected cohort variant to merge into 计算机类, got %+v", counts)
	}
}

func TestAggregateEvidenceSummariesMergesNormalizedVariantsForAdmin(t *testing.T) {
	summaries := aggregateEvidenceSummaries([]majorEvidenceCountRow{
		{EnrollmentYear: "2026", MajorCode: "315", NormalizedMajor: "计算机类", Major: "计算机类", Count: 2},
		{EnrollmentYear: "2026", MajorCode: "315", NormalizedMajor: "计算机类", Major: "计算机类（卓越班）", Count: 1},
		{EnrollmentYear: "2026", MajorCode: "315", NormalizedMajor: "计算机科学与技术", Major: "计算机科学与技术", Count: 2},
	})
	if len(summaries) != 1 || summaries[0].TotalSamples != 5 || len(summaries[0].MajorCounts) != 2 {
		t.Fatalf("unexpected admin evidence summary: %+v", summaries)
	}
	if summaries[0].MajorCounts[0].Major != "计算机类" || summaries[0].MajorCounts[0].Count != 3 {
		t.Fatalf("admin summary did not merge normalized variants: %+v", summaries[0].MajorCounts)
	}
}
