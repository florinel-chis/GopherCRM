import { expect } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

// Under full-suite CPU load the option list (debounce plus fetch) and the
// re-render after a pick can outlast the default one-second wait, so both
// steps get a longer budget. The assertions themselves are unchanged.
const AUTOCOMPLETE_TIMEOUT_MS = 3000;

/**
 * Picks an option from an MUI Autocomplete the way a user does: focus the
 * input, type, click the option once it is listed, then wait until the input
 * shows the picked value. Returns the input for further assertions.
 *
 * The focus comes first because MUI resets the text of an unfocused
 * Autocomplete input to the selected value, so an unfocused change would be
 * thrown away.
 */
export const pickAutocompleteOption = async (
  label: string | RegExp,
  typed: string,
  optionName: string,
  expectedValue: string = optionName
): Promise<HTMLElement> => {
  const input = screen.getByLabelText(label);
  input.focus();
  fireEvent.change(input, { target: { value: typed } });
  fireEvent.click(
    await screen.findByRole('option', { name: optionName }, { timeout: AUTOCOMPLETE_TIMEOUT_MS })
  );
  await waitFor(() => expect(input).toHaveValue(expectedValue), {
    timeout: AUTOCOMPLETE_TIMEOUT_MS,
  });
  return input;
};
