import { faker } from '@faker-js/faker';

export interface TestUser {
  firstName: string;
  lastName: string;
  email: string;
  password: string;
}

export function generateTestUser(): TestUser {
  return {
    firstName: faker.person.firstName(),
    lastName: faker.person.lastName(),
    email: `test_${Date.now()}_${faker.string.alphanumeric(5)}@example.com`,
    password: 'TestPassword123',
  };
}

// For easy cleanup, prefix all test emails
export function isTestEmail(email: string): boolean {
  return email.startsWith('test_') && email.endsWith('@example.com');
}

// Generate a user with specific password requirements
export function generateUserWithPassword(password: string): TestUser {
  const user = generateTestUser();
  return { ...user, password };
}

// Test data for various scenarios
export const testPasswords = {
  valid: 'TestPassword123',
  tooShort: 'Test1',
  noUppercase: 'testpassword123',
  noLowercase: 'TESTPASSWORD123',
  noNumber: 'TestPassword',
  allRequirements: 'ValidPass123!',
};