package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		bad(w, "입력 내용을 확인해 주세요")
		return false
	}
	return true
}
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		bad(w, "잘못된 항목이에요")
		return 0, false
	}
	return id, true
}
func changed(w http.ResponseWriter, res sql.Result) {
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "항목을 찾을 수 없어요"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func notFoundOrFail(a *application, w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "항목을 찾을 수 없어요"})
	} else {
		a.fail(w, err)
	}
}
func validOrError(w http.ResponseWriter, err error) bool {
	if err != nil {
		bad(w, err.Error())
		return false
	}
	return true
}
func bad(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}
func (a *application) fail(w http.ResponseWriter, err error) {
	log.Print(err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "잠시 후 다시 시도해 주세요"})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func parsePeriod(p string) (int, time.Month, error) {
	t, e := time.Parse("2006-01", p)
	return t.Year(), t.Month(), e
}
func nextPayment(now time.Time, day int, cycle, startedAt string) string {
	month := now.Month()
	var anchor time.Time
	if len(startedAt) >= 10 {
		anchor, _ = time.ParseInLocation("2006-01-02", startedAt[:10], now.Location())
	}
	if cycle == "yearly" && !anchor.IsZero() {
		if started, err := time.ParseInLocation("2006-01-02", startedAt[:10], now.Location()); err == nil {
			month = started.Month()
		}
	}
	last := time.Date(now.Year(), month+1, 0, 0, 0, 0, 0, now.Location()).Day()
	candidate := time.Date(now.Year(), month, min(day, last), 0, 0, 0, 0, now.Location())
	if !anchor.IsZero() && candidate.Before(anchor) {
		candidate = anchor
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for candidate.Before(today) {
		if cycle == "yearly" {
			candidate = candidate.AddDate(1, 0, 0)
		} else {
			candidate = candidate.AddDate(0, 1, 0)
		}
	}
	return candidate.Format("2006-01-02")
}
func initial(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return "S"
	}
	return strings.ToUpper(string(r[0]))
}
func money(v int64, currency string) string {
	symbol := map[string]string{"KRW": "₩", "USD": "$", "JPY": "¥", "EUR": "€", "TRY": "₺", "ARS": "ARS $"}[strings.ToUpper(currency)]
	if symbol == "" {
		return strings.ToUpper(currency) + " " + formatMinorUnits(v, currency)
	}
	return symbol + formatMinorUnits(v, currency)
}

func currencyFractionDigits(currency string) int {
	currency = strings.ToUpper(currency)
	if strings.Contains(" BIF CLP DJF GNF ISK JPY KMF KRW PYG RWF UGX VND VUV XAF XOF XPF ", " "+currency+" ") {
		return 0
	}
	if strings.Contains(" BHD IQD JOD KWD LYD OMR TND ", " "+currency+" ") {
		return 3
	}
	return 2
}

func minorUnitFactor(currency string) int64 {
	factor := int64(1)
	for i := 0; i < currencyFractionDigits(currency); i++ {
		factor *= 10
	}
	return factor
}

func formatMinorUnits(v int64, currency string) string {
	factor := minorUnitFactor(currency)
	if factor == 1 {
		return formatNumber(v)
	}
	return formatNumber(v/factor) + "." + fmt.Sprintf("%0*d", currencyFractionDigits(currency), v%factor)
}
func formatNumber(v int64) string {
	s := strconv.FormatInt(v, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
