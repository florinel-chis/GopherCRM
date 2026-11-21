package service

import (
	"context"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/webhook"
)

// applySubmissionLead links the submission to a lead: the newest live lead with
// the same address gains a note recording this submission, and when there is
// none a lead is created from the mapped fields. Both repositories are
// transaction-scoped by the caller; events (nil without automation) records
// the lead's outbox event in the same transaction, origin form, followed by
// one lead_form_submitted event carrying the form and submission ids (#220).
// hooks (webhook.NopTx without webhooks) emits lead.created for a new lead
// only, never for a repeat enquiry (#345).
func (s *formService) applySubmissionLead(ctx context.Context, leadRepo repository.LeadRepository, events repository.AutomationRepository, hooks webhook.TxEmitter, form *models.Form, submission *models.FormSubmission) error {
	notes := submissionNotes(form, submission)

	existing, err := leadRepo.GetLatestByEmail(submission.Email)
	if err != nil && !apperrors.IsNotFound(err) {
		return err
	}

	if err == nil && existing != nil {
		existing.Notes = appendLeadNotes(existing.Notes, notes, submissionNotesPointer(form))
		if err := leadRepo.Update(existing); err != nil {
			return err
		}
		if err := automationLeadUpdated(events, existing.ID, existing.Status, existing.Status, models.AutomationOriginForm, nil); err != nil {
			return err
		}
		if err := automationLeadFormSubmitted(events, existing.ID, form.ID, submission.ID); err != nil {
			return err
		}
		submission.LeadID = &existing.ID
		return nil
	}

	values := submission.Data
	firstName := values["first_name"]
	lastName := values["last_name"]
	if firstName == "" && lastName == "" {
		// Most forms collect one "name" field instead of a first/last pair;
		// split it on the first space so the lead carries the visitor's name.
		if parts := strings.Fields(values["name"]); len(parts) > 0 {
			firstName = truncate(parts[0], leadNameMaxLength)
			lastName = truncate(strings.Join(parts[1:], " "), leadNameMaxLength)
		}
	}
	// leads.first_name and leads.last_name are NOT NULL, and a form is free to
	// collect no name at all, so both fall back to something a salesperson can
	// still recognise on a list.
	if firstName == "" {
		firstName = "Form"
	}
	if lastName == "" {
		lastName = truncate(emailLocalPart(submission.Email), leadNameMaxLength)
	}

	lead := &models.Lead{
		FirstName: firstName,
		LastName:  lastName,
		Email:     submission.Email,
		Phone:     values["phone"],
		Company:   values["company"],
		Position:  values["position"],
		Source:    truncate(form.Name, formLeadSourceMaxLength),
		Status:    models.LeadStatusNew,
		OwnerID:   form.DefaultOwnerID,
		Notes:     appendLeadNotes("", notes, submissionNotesPointer(form)),
	}
	if err := leadRepo.Create(lead); err != nil {
		return err
	}
	if err := automationLeadCreated(events, lead.ID, models.AutomationOriginForm, nil); err != nil {
		return err
	}
	if err := automationLeadFormSubmitted(events, lead.ID, form.ID, submission.ID); err != nil {
		return err
	}
	if err := hooks.Created(ctx, webhook.Lead, lead.ID); err != nil {
		return err
	}

	submission.LeadID = &lead.ID
	return nil
}

// submissionNotes renders the note block appended to the lead: a dated header
// naming the form, followed by the answers that have no column of their own.
func submissionNotes(form *models.Form, submission *models.FormSubmission) string {
	var b strings.Builder
	b.WriteString("\n\n--- Form submission: ")
	// The name is admin-chosen, but it must not forge a marker either.
	b.WriteString(neutraliseNotesHeader(form.Name))
	b.WriteString(" (")
	b.WriteString(time.Now().Format("2006-01-02"))
	b.WriteString(") ---")

	for _, line := range submissionLines(form, submission, true) {
		b.WriteString("\n")
		b.WriteString(neutraliseNotesHeader(line))
	}
	return b.String()
}

// submissionNotesPointer is what a lead's notes get instead of a submission
// block when staff notes leave no room for one.
func submissionNotesPointer(form *models.Form) string {
	return "\n\n[Form submission: " + neutraliseNotesHeader(form.Name) + " (" + time.Now().Format("2006-01-02") +
		") not copied here because the notes are full; it is kept with the form.]"
}

// submissionLines renders the submitted answers as "Label: value" lines in the
// order the form declares them. With skipLeadFields set, the fields that map
// onto lead columns are left out, since they are already stored as columns.
func submissionLines(form *models.Form, submission *models.FormSubmission, skipLeadFields bool) []string {
	lines := make([]string, 0, len(form.Fields))
	for i := range form.Fields {
		field := form.Fields[i]
		if skipLeadFields && formLeadFields[field.Name] {
			continue
		}
		value := submission.Data[field.Name]
		if value == "" {
			continue
		}
		lines = append(lines, fieldLabel(field)+": "+value)
	}
	return lines
}
