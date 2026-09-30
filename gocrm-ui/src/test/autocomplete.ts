import { expect } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

// Under full-suite CPU load the option list (fetch plus render) and the
// re-render after a pick can outlast the default one-second wait, so both
// steps get a longer budget. The assertions themselves are unchanged.
const AUTOCOMPLETE_TIMEOUT_MS = 3000;

/**
 * Picks an option from an MUI Autocomplete without typing: focus the input,
 * open the list by pressing on it, click the option once it is listed, then
 * wait until the input shows the picked value. Returns the input for further
 * assertions.
 *
 * Nothing is typed on purpose. A server-side picker such as
 * CompanyAutocomplete fetches once when it opens and again, debounced, after
 * every keystroke; the second response unmounts the list built from the
 * first, so an option found after typing can be detached by the time it is
 * clicked and the click selects nothing. Opening alone issues one request,
 * so the option that turns up is the one that stays. Tests about
 * search-as-you-type live with the component and wait for the typed request
 * to reach the API before they click.
 */
export const pickAutocompleteOption = async (
  label: string | RegExp,
  optionName: string,
  expectedValue: string = optionName
): Promise<HTMLElement> => {
  const input = screen.getByLabelText(label);
  input.focus();
  fireEvent.mouseDown(input);
  fireEvent.click(
    await screen.findByRole('option', { name: optionName }, { timeout: AUTOCOMPLETE_TIMEOUT_MS })
  );
  await waitFor(() => expect(input).toHaveValue(expectedValue), {
    timeout: AUTOCOMPLETE_TIMEOUT_MS,
  });
  return input;
};
