package radar

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/smtp"
	"sort"
	"strings"
	"time"
)

func RenderICS(events []Event) string {
	var builder strings.Builder
	builder.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Luca Becker//Munich Event Radar//EN\r\nCALSCALE:GREGORIAN\r\nMETHOD:PUBLISH\r\nX-WR-CALNAME:Munich Event Radar\r\nX-WR-TIMEZONE:Europe/Berlin\r\n")
	for _, event := range events {
		builder.WriteString("BEGIN:VEVENT\r\n")
		writeICS(&builder, "UID", event.UID)
		writeICS(&builder, "DTSTAMP", event.UpdatedAt.UTC().Format("20060102T150405Z"))
		writeICS(&builder, "DTSTART", event.StartsAt.UTC().Format("20060102T150405Z"))
		writeICS(&builder, "DTEND", event.EndsAt.UTC().Format("20060102T150405Z"))
		writeICS(&builder, "SUMMARY", event.Title)
		writeICS(&builder, "DESCRIPTION", strings.TrimSpace(event.Description+"\n\nSource: "+event.Source))
		writeICS(&builder, "LOCATION", event.Location)
		if event.URL != "" {
			writeICS(&builder, "URL", event.URL)
		}
		writeICS(&builder, "STATUS", string(event.Status))
		builder.WriteString("END:VEVENT\r\n")
	}
	builder.WriteString("END:VCALENDAR\r\n")
	return builder.String()
}

func writeICS(builder *strings.Builder, key, value string) {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, ";", `\;`)
	value = strings.ReplaceAll(value, ",", `\,`)
	fmt.Fprintf(builder, "%s:%s\r\n", key, value)
}

func BuildDigest(events []Event, now time.Time) string {
	var upcoming []Event
	limit := now.AddDate(0, 0, 56)
	for _, event := range events {
		if event.StartsAt.After(now) && event.StartsAt.Before(limit) {
			upcoming = append(upcoming, event)
		}
	}
	sort.Slice(upcoming, func(i, j int) bool { return upcoming[i].StartsAt.Before(upcoming[j].StartsAt) })
	var builder strings.Builder
	fmt.Fprintf(&builder, "Munich Event Radar — %d upcoming event(s)\n\n", len(upcoming))
	for _, event := range upcoming {
		tentative := ""
		if event.Status == StatusTentative {
			tentative = " [TENTATIVE]"
		}
		fmt.Fprintf(&builder, "%s%s\n%s · %s\n%s\n%s\n\n", event.Title, tentative, event.StartsAt.In(time.FixedZone("CEST", 2*3600)).Format("Mon, 02 Jan 15:04"), event.Location, event.URL, event.Source)
	}
	return builder.String()
}

func DigestHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func SendDigest(config Config, content string, dryRun bool) error {
	if dryRun {
		return nil
	}
	if config.SMTPHost == "" || config.SMTPUsername == "" || config.SMTPPassword == "" || config.SMTPFrom == "" || config.DigestRecipient == "" {
		return fmt.Errorf("SMTP delivery is not configured")
	}
	host, _, hasPort := strings.Cut(config.SMTPHost, ":")
	if !hasPort {
		return fmt.Errorf("RADAR_SMTP_HOST must be host:port")
	}
	auth := smtp.PlainAuth("", config.SMTPUsername, config.SMTPPassword, host)
	message := []byte("To: " + config.DigestRecipient + "\r\nFrom: " + config.SMTPFrom + "\r\nSubject: Munich Event Radar\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + content)
	return smtp.SendMail(config.SMTPHost, auth, config.SMTPFrom, []string{config.DigestRecipient}, message)
}
