package main

import (
	"strings"

	"github.com/meshcore-analyzer/dbconfig"
)

func (db *DB) crossBorderDashboardObserverSelection(snapshot crossBorderSnapshot, dashboard bool) []crossBorderSelectedObserver {
	if !dashboard || db.Backend() != dbconfig.Postgres || !db.isV3 || !snapshot.ObserverSelectionComplete || len(snapshot.SelectedObserverRows) > crossBorderDashboardMetadata {
		return nil
	}
	seenIDs, seenRows := map[string]bool{}, map[int64]bool{}
	for _, observer := range snapshot.SelectedObserverRows {
		if seenIDs[observer.ID] || seenRows[observer.RowID] {
			return nil
		}
		seenIDs[observer.ID] = true
		seenRows[observer.RowID] = true
	}
	return append([]crossBorderSelectedObserver{}, snapshot.SelectedObserverRows...)
}

// A nil selection preserves the IATA query; a non-nil empty selection is a
// complete empty cohort. Values stay bound parameters, never SQL literals.
func (db *DB) crossBorderObserverWhere(iatas []string, selection []crossBorderSelectedObserver, column string, args *[]any) string {
	if selection == nil {
		return crossBorderIn(db, "UPPER(TRIM(COALESCE(ob.iata,'')))", iatas, args)
	}
	if len(selection) == 0 {
		return "1=0"
	}
	parameters := make([]string, len(selection))
	for i, observer := range selection {
		*args = append(*args, observer.RowID)
		parameters[i] = db.parameter(len(*args))
	}
	return column + " IN (" + strings.Join(parameters, ",") + ")"
}

func crossBorderSelectionIATAs(selection []crossBorderSelectedObserver) map[string]string {
	if selection == nil {
		return nil
	}
	result := make(map[string]string, len(selection))
	for _, observer := range selection {
		result[observer.ID] = observer.IATA
	}
	return result
}

func crossBorderSelectedIATA(selection map[string]string, id, current string) (string, bool) {
	if selection == nil {
		return current, true
	}
	region, ok := selection[id]
	return region, ok
}

func (db *DB) crossBorderSelectionArgs(args []any, selection []crossBorderSelectedObserver) []any {
	if selection == nil {
		return args
	}
	return db.planWithValues(args)
}
