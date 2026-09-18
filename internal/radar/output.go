package radar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/smtp"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

func RenderICS(branding Branding, events []Event) string {
	var builder strings.Builder
	builder.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:" + branding.CalendarProdID + "\r\nCALSCALE:GREGORIAN\r\nMETHOD:PUBLISH\r\nX-WR-CALNAME:" + branding.AppName + "\r\nX-WR-TIMEZONE:" + branding.Timezone + "\r\n")
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

func BuildDigest(branding Branding, events []Event, now time.Time) string {
	var upcoming []Event
	limit := now.AddDate(0, 0, 56)
	for _, event := range events {
		if event.StartsAt.After(now) && event.StartsAt.Before(limit) {
			upcoming = append(upcoming, event)
		}
	}
	sort.Slice(upcoming, func(i, j int) bool { return upcoming[i].StartsAt.Before(upcoming[j].StartsAt) })
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s — %d upcoming event(s)\n\n", branding.AppName, len(upcoming))
	location, err := time.LoadLocation(branding.Timezone)
	if err != nil {
		location = time.UTC
	}
	for _, event := range upcoming {
		tentative := ""
		if event.Status == StatusTentative {
			tentative = " [TENTATIVE]"
		}
		fmt.Fprintf(&builder, "%s%s\n%s · %s\n%s\n%s\n\n", event.Title, tentative, event.StartsAt.In(location).Format("Mon, 02 Jan 15:04"), event.Location, event.URL, event.Source)
	}
	return builder.String()
}

func DigestHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func SendDigest(ctx context.Context, delivery Delivery, branding Branding, content string, dryRun bool) error {
	_, span := otel.Tracer("event-radar").Start(ctx, "smtp.send")
	defer span.End()
	if err := sendDigest(delivery, branding, content, dryRun); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

func sendDigest(delivery Delivery, branding Branding, content string, dryRun bool) error {
	if dryRun {
		return nil
	}
	if delivery.SMTPHost == "" || delivery.SMTPUsername == "" || delivery.SMTPPassword == "" || delivery.SMTPFrom == "" || delivery.DigestRecipient == "" {
		return fmt.Errorf("SMTP delivery is not configured")
	}
	host, _, hasPort := strings.Cut(delivery.SMTPHost, ":")
	if !hasPort {
		return fmt.Errorf("RADAR_SMTP_HOST must be host:port")
	}
	auth := smtp.PlainAuth("", delivery.SMTPUsername, delivery.SMTPPassword, host)
	message := []byte("To: " + delivery.DigestRecipient + "\r\nFrom: " + delivery.SMTPFrom + "\r\nSubject: " + branding.AppName + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + content)
	return smtp.SendMail(delivery.SMTPHost, auth, delivery.SMTPFrom, []string{delivery.DigestRecipient}, message)
}
