package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"time"
)

// exportTables whitelists exportable tables: columns and the time column
// used for from/to filtering.
var exportTables = map[string]struct {
	cols    []string
	timeCol string
}{
	"ping_samples":      {[]string{"target_id", "ts", "rtt_us", "success", "during_speedtest"}, "ts"},
	"ping_rollup_1m":    {[]string{"target_id", "ts_bucket", "sent", "lost", "rtt_avg_us", "rtt_min_us", "rtt_max_us", "rtt_p95_us", "jitter_us"}, "ts_bucket"},
	"ping_rollup_1h":    {[]string{"target_id", "ts_bucket", "sent", "lost", "rtt_avg_us", "rtt_min_us", "rtt_max_us", "rtt_p95_us", "jitter_us"}, "ts_bucket"},
	"speedtest_results": {[]string{"id", "engine", "server_name", "server_id", "download_bps", "upload_bps", "latency_ms", "loaded_latency_ms", "packet_loss", "ran_at", "duration_ms", "error"}, "ran_at"},
	"outage_events":     {[]string{"id", "target_id", "started_at", "ended_at", "duration_ms"}, "started_at"},
	"targets":           {[]string{"id", "name", "host", "tier", "sort_order", "enabled", "created_at"}, "created_at"},
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if f := r.URL.Query().Get("format"); f != "" && f != "csv" {
		writeErr(w, http.StatusBadRequest, "only format=csv is supported")
		return
	}
	table := r.URL.Query().Get("table")
	spec, ok := exportTables[table]
	if !ok {
		writeErr(w, http.StatusBadRequest, "table must be one of ping_samples, ping_rollup_1m, ping_rollup_1h, speedtest_results, outage_events, targets")
		return
	}
	from, to := queryRange(r, 30*24*time.Hour)

	cols := ""
	for i, c := range spec.cols {
		if i > 0 {
			cols += ", "
		}
		cols += c
	}
	q := fmt.Sprintf(`SELECT %s FROM %s WHERE %s >= ? AND %s <= ? ORDER BY %s`,
		cols, table, spec.timeCol, spec.timeCol, spec.timeCol)
	rows, err := s.store.DB().QueryContext(r.Context(), q, from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.csv", table))
	cw := csv.NewWriter(w)
	cw.Write(spec.cols)

	vals := make([]any, len(spec.cols))
	ptrs := make([]any, len(spec.cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	record := make([]string, len(spec.cols))
	n := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			s.log.Error("export scan", "err", err)
			return
		}
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
				record[i] = ""
			case []byte:
				record[i] = string(x)
			default:
				record[i] = fmt.Sprint(x)
			}
		}
		cw.Write(record)
		if n++; n%1000 == 0 {
			cw.Flush() // stream in chunks
		}
	}
	cw.Flush()
}
