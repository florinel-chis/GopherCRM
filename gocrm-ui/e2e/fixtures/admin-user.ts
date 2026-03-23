import { faker } from '@faker-js/faker';

export interface AdminUser {
  firstName: string;
  lastName: string;
  email: string;
  password: string;
  role: 'admin';
}

export function generateAdminUser(): AdminUser {
  return {
    firstName: faker.person.firstName(),
    lastName: faker.person.lastName(),
    email: `admin_${Date.now()}_${faker.string.alphanumeric(6)}@example.com`,
    password: 'AdminPass123!',
    role: 'admin'
  };
}

export const testAdminCredentials = {
  email: 'test-admin@gocrm.test',
  password: 'AdminPass123!'
};

// Test data for entities — field names use camelCase to match page object fillXxxForm() parameters
export function generateLeadData() {
  return {
    companyName: faker.company.name(),
    contactName: `${faker.person.firstName()} ${faker.person.lastName()}`,
    email: `lead_${Date.now()}_${faker.string.alphanumeric(4)}@example.com`,
    phone: faker.phone.number(),
    source: faker.helpers.arrayElement(['website', 'referral', 'social_media', 'cold_call', 'email_campaign']),
    status: faker.helpers.arrayElement(['new', 'contacted', 'qualified']),
    notes: faker.lorem.paragraph()
  };
}

export function generateCustomerData() {
  return {
    companyName: faker.company.name(),
    contactName: `${faker.person.firstName()} ${faker.person.lastName()}`,
    email: `cust_${Date.now()}_${faker.string.alphanumeric(4)}@example.com`,
    phone: faker.phone.number(),
    industry: faker.commerce.department(),
    website: faker.internet.url(),
    address: faker.location.streetAddress(),
    city: faker.location.city(),
    state: faker.location.state(),
    postalCode: faker.location.zipCode(),
    country: faker.location.country(),
    notes: faker.lorem.paragraph()
  };
}

export function generateTicketData() {
  return {
    subject: faker.lorem.sentence(),
    description: faker.lorem.paragraphs(2),
    priority: faker.helpers.arrayElement(['low', 'medium', 'high']),
    status: faker.helpers.arrayElement(['open', 'in_progress']),
  };
}

export function generateTaskData() {
  return {
    title: faker.lorem.sentence(),
    description: faker.lorem.paragraph(),
    priority: faker.helpers.arrayElement(['low', 'medium', 'high']),
    status: faker.helpers.arrayElement(['pending', 'in_progress']),
    dueDate: faker.date.future().toISOString().split('T')[0] // YYYY-MM-DD format
  };
}

export function generateUserData() {
  return {
    firstName: faker.person.firstName(),
    lastName: faker.person.lastName(),
    email: `user_${Date.now()}_${faker.string.alphanumeric(4)}@example.com`,
    password: 'TempPass123!',
    role: faker.helpers.arrayElement(['sales', 'support', 'customer'])
  };
}
