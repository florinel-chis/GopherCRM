# Frontend-Backend Data Model Mismatches

This document outlines the discrepancies between frontend types and backend models that need to be addressed.

## Critical Mismatches Requiring Immediate Fix

### 0. Lead Owner Assignment for Admin Users
**Issue**: Backend requires admin users to specify owner_id when creating leads
- Admin users must explicitly assign leads to a user
- Sales users automatically get leads assigned to themselves

**Current Solution**: Auto-assign to admin user creating the lead
```typescript
// In LeadForm onSubmit
const createData: CreateLeadData = {
  ...data,
  ...(user?.role === 'admin' && { owner_id: user.id }),
};
```

**Proper Solution**: Add user selector dropdown for admin users to assign leads to any user

### 1. Lead Model Field Mapping
**Issue**: Frontend and backend use different field names
- Frontend: `company_name` → Backend: `company`
- Frontend: `contact_name` → Backend: `first_name` + `last_name`

**Current Solution**: Transformation layer in `api/endpoints/leads.ts`
```typescript
// Transform on create/update
const [firstName, ...lastNameParts] = data.contact_name.split(' ');
transformedData.first_name = firstName;
transformedData.last_name = lastNameParts.join(' ');
transformedData.company = data.company_name;

// Transform on fetch
company_name: backendLead.company || '',
contact_name: `${backendLead.first_name || ''} ${backendLead.last_name || ''}`.trim(),
```

### 2. Customer Model Extra Fields
**Issue**: Frontend expects fields that don't exist in backend
- `website`, `industry`, `annual_revenue`, `employee_count`, `total_revenue` - Not in backend
- `is_active` - Not in backend (customers don't have active status)
- `owner_id`/`owner` - Backend doesn't have owner concept for customers

**Recommendation**: Either:
- Add these fields to backend Customer model, OR
- Remove them from frontend and UI forms

### 3. Ticket Model Discrepancies
**Issue**: Field naming and missing features
- Frontend: `subject` → Backend: `title`
- Frontend expects `comments` array but backend has no comment model
- Frontend tracks `created_by` but backend doesn't

**Recommendation**: 
- Rename frontend field to match backend
- Implement comments feature in backend or remove from frontend
- Add `created_by` tracking in backend

## Form Error Handling Improvements

### Current Implementation (LeadForm)
✅ Now properly handles validation errors:
- Maps backend field names to frontend field names
- Displays field-specific error messages
- Preserves form data on error
- Shows generic error message for non-validation errors

### Recommended Pattern for Other Forms
```typescript
onError: (error: any) => {
  if (error.response?.data?.details) {
    const validationErrors = error.response.data.details;
    const fieldMapping: Record<string, string> = {
      // Map backend field names to frontend
    };
    
    Object.entries(validationErrors).forEach(([field, message]) => {
      const frontendField = fieldMapping[field] || field.toLowerCase();
      methods.setError(frontendField as any, {
        type: 'server',
        message: String(message),
      });
    });
    
    showError('Please fix the validation errors');
  } else {
    showError(error.response?.data?.message || 'Failed to save');
  }
}
```

## Missing Backend Features

### 1. Lead Model
- Missing `position` field (job title)
- Missing 'lost' status (frontend has it, backend has 'unqualified')

### 2. Task Model  
- Missing relation fields in frontend (`lead_id`, `customer_id`)
- Frontend shows `assignee` but needs to fetch user data

### 3. User Model
- Frontend expects `username` but backend only has email
- Missing `last_login_at` in frontend types

## Recommendations

### High Priority
1. **Standardize field names**: Either update backend to use frontend names or vice versa
2. **Fix Customer model**: Remove extra fields from frontend or add to backend
3. **Implement Comments**: Add comment support for tickets in backend
4. **Fix status enums**: Ensure frontend and backend use same status values

### Medium Priority
1. **Add missing fields**: Add `position` to Lead model, `resolution` to frontend Ticket
2. **User references**: Ensure all owner/assignee fields properly load user data
3. **Consistent error handling**: Apply the error handling pattern to all forms

### Low Priority
1. **Type consistency**: Use consistent types (number vs uint)
2. **Remove unused fields**: Clean up fields like `key_hash` from frontend APIKey
3. **Add missing relations**: Properly type all foreign key relationships

## Next Steps

1. **Create field mapping utilities** for consistent transformation
2. **Update all forms** with proper error handling
3. **Create backend migrations** for missing fields
4. **Update API documentation** with correct field names
5. **Add integration tests** to catch future mismatches