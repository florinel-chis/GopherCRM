import { z } from 'zod';

// Matches the customer API for both customer forms and lead conversion.
export const customerWebsiteSchema = z
  .string()
  .trim()
  .max(255, 'Website must be 255 characters or fewer')
  .refine(
    (value) => {
      if (value === '') return true;
      try {
        const url = new URL(value);
        return /^https?:\/\/[^/?#\\\s]+(?:[/?#]|$)/i.test(value) && url.hostname !== '';
      } catch {
        return false;
      }
    },
    { message: 'Website must be an http(s) URL' },
  )
  .optional();
