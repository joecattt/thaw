package dashboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/export"
)

// Generate creates an HTML dashboard from export records.
func Generate(records []export.Record, rangeDays int) string {
	projects := export.ComputeTimeAllocation(records)

	// Compute daily activity
	type dayActivity struct {
		Date     string
		Sessions int
	}
	dailyMap := make(map[string]int)
	for _, r := range records {
		day := r.Timestamp.Format("2006-01-02")
		dailyMap[day]++
	}
	var daily []dayActivity
	for d, c := range dailyMap {
		daily = append(daily, dayActivity{d, c})
	}
	// Sort by date
	for i := 0; i < len(daily); i++ {
		for j := i + 1; j < len(daily); j++ {
			if daily[j].Date < daily[i].Date {
				daily[i], daily[j] = daily[j], daily[i]
			}
		}
	}

	// Hourly distribution
	hourCounts := make([]int, 24)
	maxHour := 0
	for _, r := range records {
		h := r.Timestamp.Hour()
		hourCounts[h]++
		if hourCounts[h] > maxHour {
			maxHour = hourCounts[h]
		}
	}

	// Build HTML
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>thaw dashboard</title>
<style>
@import url('https://fonts.googleapis.com/css2?family=Anybody:wdth,wght@75..150,400;75..150,700;75..150,900&family=Fira+Code:wght@400;500&display=swap');
*{margin:0;padding:0;box-sizing:border-box}
body{background:#0e3248;color:#c0e0f0;font-family:'Fira Code',monospace;padding:48px}
h1{font-family:'Anybody',sans-serif;font-weight:900;font-stretch:130%;font-size:36px;color:#e4f4ff;margin-bottom:8px;letter-spacing:-1px}
.sub{font-size:13px;color:rgba(160,210,235,0.4);margin-bottom:40px}
.section{margin-bottom:48px}
.sec-title{font-family:'Anybody',sans-serif;font-weight:700;font-stretch:115%;font-size:14px;letter-spacing:3px;text-transform:uppercase;color:#8cc0d8;margin-bottom:20px}
.stat-row{display:flex;gap:24px;margin-bottom:32px}
.stat-box{flex:1;padding:20px;background:rgba(20,80,110,0.15);border:1px solid rgba(140,200,230,0.06);border-radius:6px}
.stat-box .n{font-family:'Anybody',sans-serif;font-weight:900;font-size:32px;color:#e4f4ff}
.stat-box .l{font-size:10px;letter-spacing:2px;text-transform:uppercase;color:rgba(160,210,235,0.3);margin-top:6px}
.bar-chart{display:flex;align-items:flex-end;gap:2px;height:120px;margin-bottom:8px}
.bar{background:#34d399;border-radius:2px 2px 0 0;min-width:8px;flex:1;transition:height 0.3s}
.bar:hover{background:#5ee8b5}
.bar-labels{display:flex;gap:2px;font-size:9px;color:rgba(160,210,235,0.25)}
.bar-labels span{flex:1;text-align:center}
.proj-row{display:flex;align-items:center;gap:16px;padding:12px 16px;background:rgba(20,80,110,0.1);border:1px solid rgba(140,200,230,0.04);border-radius:4px;margin-bottom:4px}
.proj-row .name{font-weight:600;color:#e4f4ff;min-width:180px}
.proj-row .hours{color:#34d399;min-width:80px}
.proj-row .bar-h{flex:1;height:6px;background:rgba(140,200,230,0.06);border-radius:3px;overflow:hidden}
.proj-row .bar-fill{height:100%;background:#34d399;border-radius:3px}
.footer{margin-top:48px;font-size:10px;color:rgba(160,210,235,0.15);letter-spacing:3px;text-transform:uppercase}
</style></head><body>
`)

	b.WriteString(fmt.Sprintf(`<h1>thaw dashboard</h1><div class="sub">%d-day report — generated %s</div>`,
		rangeDays, time.Now().Format("Jan 2, 2006 3:04 PM")))

	// Summary stats
	totalSessions := len(records)
	totalProjects := len(projects)
	var totalHours float64
	for _, p := range projects {
		totalHours += p.EstHours
	}
	activeDays := len(dailyMap)

	b.WriteString(`<div class="stat-row">`)
	writeStatBox(&b, fmt.Sprintf("%.0fh", totalHours), "Est. hours")
	writeStatBox(&b, fmt.Sprintf("%d", totalSessions), "Sessions")
	writeStatBox(&b, fmt.Sprintf("%d", totalProjects), "Projects")
	writeStatBox(&b, fmt.Sprintf("%d", activeDays), "Active days")
	b.WriteString(`</div>`)

	// Hourly distribution
	b.WriteString(`<div class="section"><div class="sec-title">Activity by hour</div><div class="bar-chart">`)
	for h := 0; h < 24; h++ {
		pct := 0
		if maxHour > 0 {
			pct = hourCounts[h] * 100 / maxHour
		}
		if pct < 2 && hourCounts[h] > 0 {
			pct = 2
		}
		b.WriteString(fmt.Sprintf(`<div class="bar" style="height:%d%%" title="%dh: %d sessions"></div>`, pct, h, hourCounts[h]))
	}
	b.WriteString(`</div><div class="bar-labels">`)
	for h := 0; h < 24; h++ {
		b.WriteString(fmt.Sprintf(`<span>%d</span>`, h))
	}
	b.WriteString(`</div></div>`)

	// Project breakdown
	b.WriteString(`<div class="section"><div class="sec-title">Time by project</div>`)
	maxCount := 0
	for _, p := range projects {
		if p.Count > maxCount {
			maxCount = p.Count
		}
	}
	for _, p := range projects {
		pct := 0
		if maxCount > 0 {
			pct = p.Count * 100 / maxCount
		}
		b.WriteString(fmt.Sprintf(`<div class="proj-row"><span class="name">%s</span><span class="hours">%.1fh</span><div class="bar-h"><div class="bar-fill" style="width:%d%%"></div></div></div>`,
			p.Name, p.EstHours, pct))
	}
	b.WriteString(`</div>`)

	b.WriteString(fmt.Sprintf(`<div class="footer">thaw v3.3.0 — %d records analyzed</div>`, len(records)))
	b.WriteString(`</body></html>`)

	return b.String()
}

func writeStatBox(b *strings.Builder, num, label string) {
	b.WriteString(fmt.Sprintf(`<div class="stat-box"><div class="n">%s</div><div class="l">%s</div></div>`, num, label))
}
