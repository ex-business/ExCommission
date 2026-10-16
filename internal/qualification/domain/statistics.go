package domain

import (
	"time"
	// 将IANA时区库编进二进制，保证scratch容器也能执行已配置的自然周期。
	_ "time/tzdata"
)

const (
	NaturalDay   = "NATURAL_DAY"
	NaturalWeek  = "NATURAL_WEEK"
	NaturalMonth = "NATURAL_MONTH"
	Forever      = "FOREVER"
)

// StatisticsPolicy 只筛选首次业务达标时间，不重置充值/业务量累计，也不撤销已达标身份。
// 与门槛一起创建后不可覆盖，避免历史人数随着配置修改漂移。
type StatisticsPolicy struct {
	Range    string `json:"range"`
	Timezone string `json:"timezone"`
}
type StatisticsWindow struct {
	Range    string     `json:"range"`
	Timezone string     `json:"timezone"`
	Start    *time.Time `json:"start"`
	End      *time.Time `json:"end"`
	AsOf     time.Time  `json:"as_of"`
}
type StatisticsResult struct {
	Window   StatisticsWindow `json:"window"`
	AgentIDs []string         `json:"agent_ids"`
	Count    int              `json:"count"`
}

func ValidateStatistics(p *StatisticsPolicy) error {
	if p == nil {
		return nil
	}
	if p.Range != NaturalDay && p.Range != NaturalWeek && p.Range != NaturalMonth && p.Range != Forever {
		return ErrValidation
	}
	if p.Timezone == "" || p.Timezone == "Local" {
		return ErrValidation
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return ErrValidation
	}
	return nil
}
func StatisticsFor(p *StatisticsPolicy) StatisticsPolicy {
	// 旧规则没有该字段，继续永久累计；绝不把历史规则静默改为按日统计。
	if p == nil {
		return StatisticsPolicy{Range: Forever, Timezone: "UTC"}
	}
	return *p
}

// WindowAt 将asOf视为排他查询截止点，用它前一微秒定位所属自然周期。
// 日终在零点查询时仍统计刚结束的一天；自然周固定周一开始，月份不使用固定天数。
func WindowAt(p *StatisticsPolicy, asOf time.Time) (StatisticsWindow, error) {
	if err := ValidateStatistics(p); err != nil {
		return StatisticsWindow{}, err
	}
	if asOf.Year() < 1000 || asOf.Nanosecond()%1000 != 0 {
		return StatisticsWindow{}, ErrValidation
	}
	config := StatisticsFor(p)
	w := StatisticsWindow{Range: config.Range, Timezone: config.Timezone, AsOf: asOf.UTC()}
	if config.Range == Forever {
		return w, nil
	}
	loc, _ := time.LoadLocation(config.Timezone)
	local := asOf.Add(-time.Microsecond).In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	var end time.Time
	switch config.Range {
	case NaturalDay:
		end = start.AddDate(0, 0, 1)
	case NaturalWeek:
		offset := (int(local.Weekday()) + 6) % 7
		start = start.AddDate(0, 0, -offset)
		end = start.AddDate(0, 0, 7)
	case NaturalMonth:
		start = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
		end = start.AddDate(0, 1, 0)
	}
	a, b := start.UTC(), end.UTC()
	w.Start = &a
	w.End = &b
	return w, nil
}

// Contains 左闭右开筛选首次达标时间；FirstObservedAt不能替代业务达标时间。
func (w StatisticsWindow) Contains(at time.Time) bool {
	return at.Before(w.AsOf) && (w.Start == nil || !at.Before(*w.Start)) && (w.End == nil || at.Before(*w.End))
}
