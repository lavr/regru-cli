package regruapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type FlexInt struct {
	Value int
	Valid bool
}

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" {
		*f = FlexInt{}
		return nil
	}
	s := string(b)
	if len(b) > 0 && b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = strings.TrimSpace(str)
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("not an integer: %s", string(b))
	}
	*f = FlexInt{Value: v, Valid: true}
	return nil
}

type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" {
		*f = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("not a string or number: %s", string(b))
	}
	*f = FlexString(n.String())
	return nil
}

type RR struct {
	Subname string
	Rectype string
	Content string
	Prio    FlexInt
	Weight  *FlexInt
	Port    *FlexInt
	State   string
}

func (r *RR) UnmarshalJSON(b []byte) error {
	var w struct {
		Subname string   `json:"subname"`
		Rectype string   `json:"rectype"`
		Content string   `json:"content"`
		Prio    FlexInt  `json:"prio"`
		Weight  *FlexInt `json:"weight"`
		Port    *FlexInt `json:"port"`
		State   string   `json:"state"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*r = RR{Subname: w.Subname, Rectype: w.Rectype, Content: w.Content, Prio: w.Prio, Weight: w.Weight, Port: w.Port, State: w.State}
	return nil
}

type SOAStrings struct {
	TTL        string
	MinimumTTL string
}

type ZoneRecords struct {
	Domain    string
	ServiceID string
	RRs       []RR
	SOA       *SOAStrings
}

type DomainError struct {
	Domain string
	Code   string
	Text   string
	Params json.RawMessage
}

func (e *DomainError) Error() string {
	s := "reg.ru domain error " + e.Code + " for " + e.Domain
	if e.Text != "" {
		s += ": " + e.Text
	}
	return s
}

type IncompleteReadError struct {
	Method string
	Reason string
}

func (e *IncompleteReadError) Error() string {
	return "incomplete response from " + e.Method + ": " + e.Reason
}

type DomainNSS struct {
	Domain    string
	ServiceID string
	NSS       []string
	Err       *DomainError
}

type Service struct {
	Domain    string
	ServiceID string
	State     string
}
