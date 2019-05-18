// Package scheduler is a cron replacement based on:
//  http://adam.herokuapp.com/past/2010/4/13/rethinking_cron/
// and
//  https://github.com/dbader/schedule
//
// Uses include:
//  func main() {
//    job := func() {
//	fmt.Println("Time's up!")
//    }
//    scheduler.Every(5).Seconds().Run(function)
//    scheduler.Every().Day().Run(function)
//    scheduler.Every().Sunday().At("08:30").Run(function)
//  }
package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Pallinder/go-randomdata"
)

type scheduled interface {
	nextRun() (time.Duration, error)
}

// Job defines a running job and allows to stop a scheduled job or run it.
type Job struct {
	fn        func()
	Quit      chan bool
	SkipWait  chan bool
	err       error
	schedule  scheduled
	isRunning bool
	sync.RWMutex
}

type recurrent struct {
	units    int
	period   time.Duration
	done     bool
	minTimes int
	maxTimes int
}

func (r *recurrent) nextRun() (time.Duration, error) {
	if r.units == 0 || r.period == 0 {
		return 0, errors.New("cannot set recurrent time with 0")
	}
	if !r.done {
		r.done = true
		return 0, nil
	}

	if r.minTimes > 0 && r.maxTimes > 0 {
		v := int(time.Duration(randomdata.Number(r.minTimes, r.maxTimes)))
		return time.Duration(v) * r.period, nil
	}
	return time.Duration(r.units) * r.period, nil
}

type daily struct {
	hour        int
	min         int
	sec         int
	randMinDay  int
	randMaxDay  int
	startTime   string
	endTime     string
	minInterval int
	maxInterval int
}

func (d *daily) setTime(h, m, s int) {
	d.hour = h
	d.min = m
	d.sec = s
}

func (d daily) nextRun() (time.Duration, error) {
	now := time.Now()
	year, month, day := now.Date()

	if d.startTime == "" {
		date := time.Date(year, month, day, d.hour, d.min, d.sec, 0, time.Local)
		if now.Before(date) { // 现在时间还没到你设的时间，那等就是了
			return date.Sub(now), nil
		}

		// 现在时间已经过了，你设的时间，则将时间更新到明天
		date = time.Date(year, month, day+1, d.hour, d.min, d.sec, 0, time.Local)
		return date.Sub(now), nil
	}

	hour1, min1, sec1, err := parseTime(d.startTime)
	if err != nil {
		return 0, err
	}
	date1 := time.Date(year, month, day, hour1, min1, sec1, 0, time.Local)
	if now.Before(date1) { // 现在时间还没到你设的时间，那等就是了
		return date1.Sub(now), nil
	}

	hour2, min2, sec2, err := parseTime(d.endTime)
	if err != nil {
		return 0, err
	}
	date2 := time.Date(year, month, day, hour2, min2, sec2, 0, time.Local)
	if now.After(date2) { // 现在时间已经过了你设定最晚时间，设到下一个随机天的开始时间就是了
		days := 1
		if d.randMinDay > 0 && d.randMaxDay > 0 {
			days = int(time.Duration(randomdata.Number(d.randMinDay, d.randMaxDay)))
		}

		date2 = time.Date(year, month, day+days, hour1, min1, sec1, 0, time.Local)
		return date2.Sub(now), nil
	}

	// 时间范围内，则按随机范围内等就是了
	seconds := 0
	if d.minInterval > 0 {
		seconds = int(time.Duration(randomdata.Number(d.minInterval, d.maxInterval)))
	}
	date := time.Date(year, month, day, now.Hour(), now.Minute(), now.Second()+seconds, 0, time.Local)
	return date.Sub(now), nil
}

type weekly struct {
	day time.Weekday
	d   daily
}

func (w weekly) nextRun() (time.Duration, error) {
	now := time.Now()
	year, month, day := now.Date()
	numDays := w.day - now.Weekday()
	if numDays == 0 {
		numDays = 7
	} else if numDays < 0 {
		numDays += 7
	}
	date := time.Date(year, month, day+int(numDays), w.d.hour, w.d.min, w.d.sec, 0, time.Local)
	return date.Sub(now), nil
}

// Every defines when to run a job. For a recurrent jobs (n seconds/minutes/hours) you
// should specify the unit and then call to the correspondent period method.
func Every(times ...int) *Job {
	switch len(times) {
	case 0:
		return &Job{}
	case 1:
		r := new(recurrent)
		r.units = times[0]
		return &Job{schedule: r}
	default:
		// Yeah... I don't like it either. But go does not support default
		// parameters nor method overloading. In an ideal world should
		// return an error at compile time not at runtime. :/
		return &Job{err: errors.New("too many arguments in Every")}
	}
}

// Every defines when to run a job. For a recurrent jobs (n seconds/minutes/hours) you
// should specify the unit and then call to the correspondent period method.
func EveryRand(times ...int) *Job {
	switch len(times) {
	case 2:
		r := new(recurrent)
		r.units = times[0]
		r.minTimes = times[0]
		r.maxTimes = times[1]
		return &Job{schedule: r}
	default:
		// Yeah... I don't like it either. But go does not support default
		// parameters nor method overloading. In an ideal world should
		// return an error at compile time not at runtime. :/
		return &Job{err: errors.New("too many arguments in Every")}
	}
}

// NotImmediately allows recurrent jobs not to be executed immediatelly after
// definition. If a job is declared hourly won't start executing until the first hour
// passed.
func (j *Job) NotImmediately() *Job {
	rj, ok := j.schedule.(*recurrent)
	if !ok {
		j.err = errors.New("【NotImmediately】 bad function chaining")
		return j
	}
	rj.done = true
	return j
}

// At lets you define a specific time when the job would be run. Does not work with
// recurrent jobs.
// Time should be defined as a string separated by a colon. Could be used as "08:35:30",
// "08:35" or "8" for only the hours.
func (j *Job) At(hourTime string) *Job {
	if j.err != nil {
		return j
	}
	hour, min, sec, err := parseTime(hourTime)
	if err != nil {
		j.err = err
		return j
	}
	d, ok := j.schedule.(daily)
	if !ok {
		w, ok := j.schedule.(weekly)
		if !ok {
			j.err = errors.New("【At】 bad function chaining")
			return j
		}
		w.d.setTime(hour, min, sec)
		j.schedule = w
	} else {
		d.setTime(hour, min, sec)
		j.schedule = d
	}
	return j
}

func (j *Job) StartAt(hourTime string) *Job {
	if j.err != nil {
		return j
	}
	d, ok := j.schedule.(daily)
	if !ok {
		w, ok := j.schedule.(weekly)
		if !ok {
			j.err = errors.New("【StartAt】 bad function chaining")
			return j
		}
		w.d.startTime = hourTime
		j.schedule = w
	} else {
		d.startTime = hourTime
		j.schedule = d
	}
	return j
}

func (j *Job) EndAt(hourTime string) *Job {
	if j.err != nil {
		return j
	}
	d, ok := j.schedule.(daily)
	if !ok {
		w, ok := j.schedule.(weekly)
		if !ok {
			j.err = errors.New("【EndAt】 bad function chaining")
			return j
		}
		w.d.endTime = hourTime
		j.schedule = w
	} else {
		d.endTime = hourTime
		j.schedule = d
	}
	return j
}

func (j *Job) Interval(times ...int) *Job {
	switch len(times) {
	case 2:
		d, ok := j.schedule.(daily)
		if !ok {
			w, ok := j.schedule.(weekly)
			if !ok {
				j.err = errors.New("【EveryRandInterval】 bad function chaining")
				return j
			}
			w.d.minInterval, w.d.maxInterval = times[0], times[1]
			j.schedule = w
		} else {
			d.minInterval, d.maxInterval = times[0], times[1]
			j.schedule = d
		}
	default:
		// Yeah... I don't like it either. But go does not support default
		// parameters nor method overloading. In an ideal world should
		// return an error at compile time not at runtime. :/
		return &Job{err: errors.New("too many arguments in Every")}
	}
	return j
}

func (j *Job) Next() time.Duration {
	d, ok := j.schedule.(daily)
	if ok {
		n, _ := d.nextRun()
		return n
	}

	w, ok := j.schedule.(weekly)
	if ok {
		n, _ := w.nextRun()
		return n
	}

	rj, ok := j.schedule.(*recurrent)
	if ok {
		n, _ := rj.nextRun()
		return n
	}
	return 0
}

// Run sets the job to the schedule and returns the pointer to the job so it may be
// stopped or executed without waiting or an error.
func (j *Job) Run(f func()) (*Job, error) {
	if j.err != nil {
		return nil, j.err
	}
	var next time.Duration
	var err error
	j.Quit = make(chan bool, 1)
	j.SkipWait = make(chan bool, 1)
	j.fn = f
	// Check for possible errors in scheduling
	next, err = j.schedule.nextRun()
	if err != nil {
		return nil, err
	}
	go func(j *Job) {
		for {
			select {
			case <-j.Quit:
				return
			case <-j.SkipWait:
				go runJob(j)
			case <-time.After(next):
				go runJob(j)
			}
			next, _ = j.schedule.nextRun()
		}
	}(j)
	return j, nil
}

func (j *Job) setRunning(running bool) {
	j.Lock()
	defer j.Unlock()

	j.isRunning = running
}

func runJob(job *Job) {
	if job.IsRunning() {
		return
	}
	job.setRunning(true)
	job.fn()
	job.setRunning(false)
}

func parseTime(str string) (hour, min, sec int, err error) {
	chunks := strings.Split(str, ":")
	var hourStr, minStr, secStr string
	switch len(chunks) {
	case 1:
		hourStr = chunks[0]
		minStr = "0"
		secStr = "0"
	case 2:
		hourStr = chunks[0]
		minStr = chunks[1]
		secStr = "0"
	case 3:
		hourStr = chunks[0]
		minStr = chunks[1]
		secStr = chunks[2]
	}
	hour, err = strconv.Atoi(hourStr)
	if err != nil {
		return 0, 0, 0, errors.New("bad time")
	}
	min, err = strconv.Atoi(minStr)
	if err != nil {
		return 0, 0, 0, errors.New("bad time")
	}
	sec, err = strconv.Atoi(secStr)
	if err != nil {
		return 0, 0, 0, errors.New("bad time")
	}

	if hour > 23 || min > 59 || sec > 59 {
		return 0, 0, 0, errors.New("bad time")
	}

	return
}

func (j *Job) dayOfWeek(d time.Weekday) *Job {
	if j.schedule != nil {
		j.err = errors.New("bad function chaining")
	}
	j.schedule = weekly{day: d}
	return j
}

// Monday sets the job to run every Monday.
func (j *Job) Monday() *Job {
	return j.dayOfWeek(time.Monday)
}

// Tuesday sets the job to run every Tuesday.
func (j *Job) Tuesday() *Job {
	return j.dayOfWeek(time.Tuesday)
}

// Wednesday sets the job to run every Wednesday.
func (j *Job) Wednesday() *Job {
	return j.dayOfWeek(time.Wednesday)
}

// Thursday sets the job to run every Thursday.
func (j *Job) Thursday() *Job {
	return j.dayOfWeek(time.Thursday)
}

// Friday sets the job to run every Friday.
func (j *Job) Friday() *Job {
	return j.dayOfWeek(time.Friday)
}

// Saturday sets the job to run every Saturday.
func (j *Job) Saturday() *Job {
	return j.dayOfWeek(time.Saturday)
}

// Sunday sets the job to run every Sunday.
func (j *Job) Sunday() *Job {
	return j.dayOfWeek(time.Sunday)
}

// Day sets the job to run every day.
func (j *Job) Day() *Job {
	if j.schedule != nil {
		j.err = errors.New("bad function chaining")
	}
	j.schedule = daily{}
	return j
}

// Day sets the job to run every day.
func (j *Job) Days() *Job {
	rj, ok := j.schedule.(*recurrent)
	if !ok {
		j.err = errors.New("【Day】bad function chaining 【recurrent】")
	}

	j.schedule = daily{randMinDay: rj.minTimes, randMaxDay: rj.maxTimes}
	return j
}

func (j *Job) timeOfDay(d time.Duration) *Job {
	if j.err != nil {
		return j
	}
	r := j.schedule.(*recurrent)
	r.period = d
	j.schedule = r
	return j
}

// Seconds sets the job to run every n Seconds where n was defined in the Every
// function.
func (j *Job) Seconds() *Job {
	d, ok := j.schedule.(daily)
	if !ok {
		w, ok := j.schedule.(weekly)
		if !ok {
			j.err = errors.New("【Seconds】 bad function chaining")
			return j
		}
		if w.d.minInterval > 0 {
			return j
		}
	} else {
		if d.minInterval > 0 {
			return j
		}
	}

	return j.timeOfDay(time.Second)
}

// Minutes sets the job to run every n Minutes where n was defined in the Every
// function.
func (j *Job) Minutes() *Job {
	return j.timeOfDay(time.Minute)
}

// Hours sets the job to run every n Hours where n was defined in the Every function.
func (j *Job) Hours() *Job {
	return j.timeOfDay(time.Hour)
}

// IsRunning returns if the job is currently running
func (j *Job) IsRunning() bool {
	j.RLock()
	defer j.RUnlock()
	return j.isRunning
}
