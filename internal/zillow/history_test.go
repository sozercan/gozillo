package zillow

import (
	"reflect"
	"testing"
)

func TestAnnotateListingHistoryMarksNewChangedAndStillActive(t *testing.T) {
	t.Parallel()

	oldPrice := int64(5000)
	newPrice := int64(4800)
	oldTotal := int64(5200)
	newTotal := int64(5000)
	previous := []Listing{
		{ID: "changed", Price: &oldPrice, TotalMonthlyCost: &oldTotal, Availability: "2026-08-01", Laundry: LaundryUnknown},
		{ID: "same", Price: &oldPrice, Availability: "2026-08-10", Laundry: LaundryInUnit, FlexSpaces: []string{"office"}},
	}
	current := []Listing{
		{ID: "new", Price: &newPrice},
		{ID: "changed", Price: &newPrice, TotalMonthlyCost: &newTotal, Availability: "2026-08-15", Laundry: LaundryInUnit},
		{ID: "same", Price: &oldPrice, Availability: "2026-08-10", Laundry: LaundryInUnit, FlexSpaces: []string{"office"}},
	}

	got := AnnotateListingHistory(current, previous)
	if got[0].HistoryStatus != HistoryNewToDigest || len(got[0].HistoryChanges) != 0 {
		t.Fatalf("new listing = %+v", got[0])
	}
	if got[1].HistoryStatus != HistoryPreviouslyChanged {
		t.Fatalf("changed listing status = %+v", got[1])
	}
	wantChanges := []string{"price decreased", "total monthly cost decreased", "availability changed", "in-unit laundry confirmed"}
	if !reflect.DeepEqual(got[1].HistoryChanges, wantChanges) {
		t.Fatalf("changes = %#v, want %#v", got[1].HistoryChanges, wantChanges)
	}
	if got[2].HistoryStatus != HistoryPreviouslyStillActive || len(got[2].HistoryChanges) != 0 {
		t.Fatalf("still-active listing = %+v", got[2])
	}
}
