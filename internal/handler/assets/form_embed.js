/*
 * GopherCRM form renderer.
 *
 * Dropped onto any page with:
 *   <script src="https://crm.example.com/api/v1/forms/public/embed.js"
 *           data-form-key="PUBLIC_ID" async></script>
 *
 * It fetches the form definition from the API it was itself served by, renders
 * the form where the tag sits, and posts the answers back. No dependencies, no
 * build step, no globals: everything below lives in one closure so that a page
 * carrying several forms runs several independent copies.
 *
 * Kept to ES2017 syntax on purpose — this runs on whatever browser a visitor
 * happens to have, and it is served as-is with no transpilation.
 */
(function () {
  'use strict';

  var SCRIPT_PATH = '/forms/public/embed.js';
  var STYLE_ID = 'gcrm-form-styles';
  var RECAPTCHA_SCRIPT_ID = 'gcrm-recaptcha-script';
  var RECAPTCHA_SRC = 'https://www.google.com/recaptcha/api.js?render=';

  /* The server names the honeypot input in every definition; this is only the
   * fallback for a definition that predates the field. */
  var DEFAULT_HONEYPOT_FIELD = 'website_url_confirm';

  /* Permissive on purpose, and identical in spirit to the server's check: the
   * point is to catch a typo before a round trip, not to rule on what a valid
   * address is. */
  var EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

  var GENERIC_ERROR = 'Something went wrong. Please try again.';
  var DEFAULT_THANK_YOU = 'Thank you. Your submission has been received.';

  var script = claimScript();
  if (!script) {
    return;
  }

  var formKey = script.getAttribute('data-form-key');
  if (!formKey) {
    console.warn('[gophercrm] the form script tag is missing its data-form-key attribute');
    return;
  }

  var apiBase = deriveApiBase(script.src);
  var container = document.createElement('div');
  container.className = 'gcrm-form';
  script.parentNode.insertBefore(container, script.nextSibling);

  var recaptchaPromise = null;

  fetchJSON(apiBase + '/' + encodeURIComponent(formKey), null)
    .then(function (result) {
      if (result.status !== 200 || !result.data) {
        /* An unknown, unpublished or origin-restricted form renders nothing at
         * all. Saying more on the page would tell a visitor about a form they
         * are not meant to see. */
        console.warn('[gophercrm] form "' + formKey + '" is not available (status ' + result.status + ')');
        return;
      }
      render(result.data);
    })
    .catch(function (error) {
      console.warn('[gophercrm] could not load form "' + formKey + '"', error);
    });

  /* ---------------------------------------------------------------- setup */

  /* document.currentScript identifies the tag being executed, including for an
   * async script. It comes back null when the code is re-run from a callback
   * or moved around by a tag manager, so fall back to the first embed tag no
   * other copy has claimed yet. */
  function claimScript() {
    var candidate = document.currentScript;
    if (!candidate) {
      var tags = document.querySelectorAll('script[data-form-key]');
      for (var i = 0; i < tags.length; i++) {
        if (!tags[i].gcrmClaimed) {
          candidate = tags[i];
          break;
        }
      }
    }
    if (!candidate || candidate.gcrmClaimed) {
      return null;
    }
    candidate.gcrmClaimed = true;
    return candidate;
  }

  /* The API is wherever this script was served from, so an embed keeps working
   * on any host, port or path prefix without being told where the CRM lives. */
  function deriveApiBase(src) {
    var clean = String(src || '').split('?')[0].split('#')[0];
    var index = clean.indexOf(SCRIPT_PATH);
    if (index !== -1) {
      return clean.slice(0, index) + '/forms/public';
    }
    return clean.replace(/\/[^/]*$/, '');
  }

  /* One stylesheet per page, however many forms it carries. Everything is
   * scoped under .gcrm-form and every value a host page might want to change
   * is a custom property, so restyling is a one-line override and no rule of
   * ours can escape into the page. */
  function ensureStyles() {
    if (document.getElementById(STYLE_ID)) {
      return;
    }
    var style = document.createElement('style');
    style.id = STYLE_ID;
    style.textContent = [
      '.gcrm-form {',
      '  --gcrm-accent: #2f6fed;',
      '  --gcrm-bg: #ffffff;',
      '  --gcrm-text: #1f2430;',
      '  --gcrm-radius: 8px;',
      '  --gcrm-font: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;',
      '  position: relative;',
      '  box-sizing: border-box;',
      '  font-family: var(--gcrm-font);',
      '  color: var(--gcrm-text);',
      '  background: var(--gcrm-bg);',
      '  line-height: 1.5;',
      '}',
      '.gcrm-form *, .gcrm-form *::before, .gcrm-form *::after { box-sizing: inherit; }',
      '.gcrm-form h2 { margin: 0 0 16px; font-size: 1.25rem; line-height: 1.3; }',
      '.gcrm-field { margin: 0 0 16px; }',
      '.gcrm-field label { display: block; margin-bottom: 6px; font-weight: 600; font-size: 0.9rem; }',
      '.gcrm-required { color: #c0392b; margin-left: 2px; }',
      '.gcrm-form input[type="text"], .gcrm-form input[type="email"], .gcrm-form input[type="tel"],',
      '.gcrm-form textarea, .gcrm-form select {',
      '  width: 100%;',
      '  padding: 10px 12px;',
      '  border: 1px solid #c8ccd6;',
      '  border-radius: var(--gcrm-radius);',
      '  background: var(--gcrm-bg);',
      '  color: var(--gcrm-text);',
      '  font: inherit;',
      '}',
      '.gcrm-form textarea { min-height: 7rem; resize: vertical; }',
      '.gcrm-form input:focus, .gcrm-form textarea:focus, .gcrm-form select:focus {',
      '  outline: none;',
      '  border-color: var(--gcrm-accent);',
      '  box-shadow: 0 0 0 3px rgba(47, 111, 237, 0.2);',
      '}',
      '.gcrm-check { display: flex; align-items: flex-start; gap: 8px; }',
      '.gcrm-check label { font-weight: 400; margin: 0; }',
      '.gcrm-check input { margin-top: 4px; }',
      '.gcrm-help { display: block; margin-top: 4px; font-size: 0.82rem; color: #667085; }',
      '.gcrm-error { display: none; margin-top: 4px; font-size: 0.82rem; color: #c0392b; }',
      '.gcrm-error.gcrm-visible { display: block; }',
      '.gcrm-invalid input, .gcrm-invalid textarea, .gcrm-invalid select { border-color: #c0392b; }',
      '.gcrm-form button {',
      '  appearance: none;',
      '  border: 0;',
      '  border-radius: var(--gcrm-radius);',
      '  background: var(--gcrm-accent);',
      '  color: #ffffff;',
      '  font: inherit;',
      '  font-weight: 600;',
      '  padding: 12px 20px;',
      '  cursor: pointer;',
      '}',
      '.gcrm-form button[disabled] { opacity: 0.6; cursor: default; }',
      '.gcrm-success { padding: 16px; border-radius: var(--gcrm-radius); background: #eaf5ec; color: #1e5631; }',
      '.gcrm-form-error { margin-bottom: 16px; color: #c0392b; font-size: 0.9rem; }',
      '.gcrm-trap {',
      '  position: absolute;',
      '  left: -9999px;',
      '  top: auto;',
      '  width: 1px;',
      '  height: 1px;',
      '  overflow: hidden;',
      '  opacity: 0;',
      '}'
    ].join('\n');
    document.head.appendChild(style);
  }

  /* --------------------------------------------------------------- render */

  function render(definition) {
    ensureStyles();

    var fields = definition.fields || [];
    var honeypotName = definition.honeypot_field || DEFAULT_HONEYPOT_FIELD;
    var controls = {};

    var form = document.createElement('form');
    form.className = 'gcrm-form-element';
    form.setAttribute('novalidate', 'novalidate');

    if (definition.name) {
      var heading = document.createElement('h2');
      heading.textContent = definition.name;
      form.appendChild(heading);
    }

    var formError = document.createElement('div');
    formError.className = 'gcrm-form-error';
    formError.style.display = 'none';
    form.appendChild(formError);

    for (var i = 0; i < fields.length; i++) {
      var built = buildField(fields[i]);
      if (!built) {
        continue;
      }
      controls[fields[i].name] = built;
      form.appendChild(built.wrapper);
    }

    var consent = null;
    if (definition.consent_text) {
      consent = buildConsent(definition.consent_text);
      form.appendChild(consent.wrapper);
    }

    /* The honeypot is offscreen rather than display:none — a bot that skips
     * hidden inputs is exactly the kind this catches, and taking it out of the
     * tab order plus off the autofill list keeps a real visitor from ever
     * touching it. */
    var trap = document.createElement('div');
    trap.className = 'gcrm-trap';
    trap.setAttribute('aria-hidden', 'true');
    var trapInput = document.createElement('input');
    trapInput.type = 'text';
    trapInput.name = honeypotName;
    trapInput.tabIndex = -1;
    trapInput.setAttribute('autocomplete', 'off');
    trap.appendChild(trapInput);
    form.appendChild(trap);

    var submit = document.createElement('button');
    submit.type = 'submit';
    submit.textContent = 'Submit';
    form.appendChild(submit);

    form.addEventListener('submit', function (event) {
      event.preventDefault();
      handleSubmit(definition, form, fields, controls, consent, trapInput, submit, formError);
    });

    container.appendChild(form);
  }

  function buildField(field) {
    var wrapper = document.createElement('div');
    wrapper.className = 'gcrm-field';

    if (field.type === 'hidden') {
      var hidden = document.createElement('input');
      hidden.type = 'hidden';
      hidden.name = field.name;
      wrapper.appendChild(hidden);
      wrapper.style.display = 'none';
      return { wrapper: wrapper, input: hidden, error: null, field: field };
    }

    var inputID = 'gcrm-' + formKey + '-' + field.name;
    var input = buildControl(field, inputID);
    var label = document.createElement('label');
    label.setAttribute('for', inputID);
    label.textContent = field.label || field.name;
    if (field.required) {
      var marker = document.createElement('span');
      marker.className = 'gcrm-required';
      marker.textContent = '*';
      label.appendChild(marker);
    }

    if (field.type === 'checkbox') {
      var check = document.createElement('div');
      check.className = 'gcrm-check';
      check.appendChild(input);
      check.appendChild(label);
      wrapper.appendChild(check);
    } else {
      wrapper.appendChild(label);
      wrapper.appendChild(input);
    }

    if (field.help_text) {
      var help = document.createElement('span');
      help.className = 'gcrm-help';
      help.textContent = field.help_text;
      wrapper.appendChild(help);
    }

    var error = document.createElement('span');
    error.className = 'gcrm-error';
    wrapper.appendChild(error);

    return { wrapper: wrapper, input: input, error: error, field: field };
  }

  function buildControl(field, inputID) {
    var input;

    if (field.type === 'textarea') {
      input = document.createElement('textarea');
    } else if (field.type === 'select') {
      input = document.createElement('select');
      var placeholder = document.createElement('option');
      placeholder.value = '';
      placeholder.textContent = field.placeholder || 'Please choose…';
      input.appendChild(placeholder);
      var options = field.options || [];
      for (var i = 0; i < options.length; i++) {
        var option = document.createElement('option');
        option.value = options[i];
        option.textContent = options[i];
        input.appendChild(option);
      }
    } else {
      input = document.createElement('input');
      input.type = field.type === 'email' ? 'email' : field.type === 'phone' ? 'tel' : field.type === 'checkbox' ? 'checkbox' : 'text';
    }

    input.id = inputID;
    input.name = field.name;
    if (field.placeholder && field.type !== 'select' && field.type !== 'checkbox') {
      input.placeholder = field.placeholder;
    }
    if (field.max_length && input.tagName !== 'SELECT' && field.type !== 'checkbox') {
      input.maxLength = field.max_length;
    }
    return input;
  }

  function buildConsent(text) {
    var wrapper = document.createElement('div');
    wrapper.className = 'gcrm-field';

    var check = document.createElement('div');
    check.className = 'gcrm-check';

    var input = document.createElement('input');
    input.type = 'checkbox';
    input.id = 'gcrm-' + formKey + '-consent';

    var label = document.createElement('label');
    label.setAttribute('for', input.id);
    /* textContent, never innerHTML: the consent text is admin-authored copy,
     * not markup, and it must not be able to inject anything into a host
     * page. */
    label.textContent = text;

    check.appendChild(input);
    check.appendChild(label);
    wrapper.appendChild(check);

    var error = document.createElement('span');
    error.className = 'gcrm-error';
    wrapper.appendChild(error);

    return { wrapper: wrapper, input: input, error: error };
  }

  /* --------------------------------------------------------------- submit */

  function handleSubmit(definition, form, fields, controls, consent, trapInput, submit, formError) {
    clearErrors(controls, consent, formError);

    var values = {};
    var firstInvalid = null;

    for (var i = 0; i < fields.length; i++) {
      var field = fields[i];
      var control = controls[field.name];
      if (!control) {
        continue;
      }

      var value = readValue(field, control.input);
      values[field.name] = value;

      var message = validateField(field, value);
      if (message) {
        showFieldError(control, message);
        firstInvalid = firstInvalid || control;
      }
    }

    var consentGiven = consent ? consent.input.checked : true;
    if (consent && !consentGiven) {
      showFieldError(consent, 'Please tick this box to continue.');
      firstInvalid = firstInvalid || consent;
    }

    if (firstInvalid) {
      if (firstInvalid.input && firstInvalid.input.focus) {
        firstInvalid.input.focus();
      }
      return;
    }

    var label = submit.textContent;
    submit.disabled = true;
    submit.textContent = 'Sending…';

    var release = function () {
      submit.disabled = false;
      submit.textContent = label;
    };

    captchaToken(definition.recaptcha_site_key).then(function (token) {
      var body = {
        values: values,
        consent: consentGiven,
        challenge: definition.challenge || '',
        captcha_token: token,
        page_url: window.location.href
      };
      body[definition.honeypot_field || DEFAULT_HONEYPOT_FIELD] = trapInput.value;

      return fetchJSON(apiBase + '/' + encodeURIComponent(formKey) + '/submissions', body);
    }).then(function (result) {
      if (result.status >= 200 && result.status < 300 && result.data) {
        succeed(form, result.data);
        return;
      }

      release();
      var details = result.body && result.body.error ? result.body.error.details : null;
      if (result.status === 400 && details && typeof details === 'object') {
        applyFieldErrors(controls, consent, formError, details);
        return;
      }
      showFormError(formError, GENERIC_ERROR);
    }).catch(function (error) {
      console.warn('[gophercrm] submission failed', error);
      release();
      showFormError(formError, GENERIC_ERROR);
    });
  }

  function readValue(field, input) {
    if (field.type === 'checkbox') {
      return input.checked ? 'true' : 'false';
    }
    return input.value.trim();
  }

  function validateField(field, value) {
    if (field.required) {
      if (field.type === 'checkbox') {
        return value === 'true' ? '' : 'Please tick this box to continue.';
      }
      if (!value) {
        return (field.label || field.name) + ' is required.';
      }
    }
    if (field.type === 'email' && value && !EMAIL_PATTERN.test(value)) {
      return 'Please enter a valid email address.';
    }
    return '';
  }

  function succeed(form, outcome) {
    if (outcome.action === 'redirect' && outcome.redirect_url) {
      window.location.href = outcome.redirect_url;
      return;
    }

    var success = document.createElement('div');
    success.className = 'gcrm-success';
    /* Whether the visitor still has to confirm their address is the server's
     * story to tell — pending_confirmation is already reflected in message. */
    success.textContent = outcome.message || DEFAULT_THANK_YOU;
    form.parentNode.replaceChild(success, form);
  }

  /* --------------------------------------------------------------- errors */

  function clearErrors(controls, consent, formError) {
    for (var name in controls) {
      if (Object.prototype.hasOwnProperty.call(controls, name)) {
        clearFieldError(controls[name]);
      }
    }
    if (consent) {
      clearFieldError(consent);
    }
    formError.textContent = '';
    formError.style.display = 'none';
  }

  function clearFieldError(control) {
    if (!control.error) {
      return;
    }
    control.error.textContent = '';
    control.error.className = 'gcrm-error';
    control.wrapper.className = control.wrapper.className.replace(' gcrm-invalid', '');
  }

  function showFieldError(control, message) {
    if (!control.error) {
      return;
    }
    control.error.textContent = message;
    control.error.className = 'gcrm-error gcrm-visible';
    if (control.wrapper.className.indexOf('gcrm-invalid') === -1) {
      control.wrapper.className += ' gcrm-invalid';
    }
  }

  /* The server keys its rejections by field name, which is exactly how the
   * controls are keyed, so each message lands next to the input that caused
   * it. Anything unrecognised — the challenge, a field this render does not
   * know — goes to the form-level line instead of being swallowed. */
  function applyFieldErrors(controls, consent, formError, details) {
    var unplaced = [];

    for (var name in details) {
      if (!Object.prototype.hasOwnProperty.call(details, name)) {
        continue;
      }
      var message = String(details[name]);
      if (controls[name]) {
        showFieldError(controls[name], message);
      } else if (name === 'consent' && consent) {
        showFieldError(consent, message);
      } else {
        unplaced.push(message);
      }
    }

    if (unplaced.length) {
      showFormError(formError, unplaced.join(' '));
    }
  }

  function showFormError(formError, message) {
    formError.textContent = message;
    formError.style.display = '';
  }

  /* ------------------------------------------------------------- captcha */

  /* reCAPTCHA is fetched only when the form actually uses it, and only when
   * the visitor is about to submit — a page that embeds a form should not pay
   * for a third-party script nobody triggers. */
  function captchaToken(siteKey) {
    if (!siteKey) {
      return Promise.resolve('');
    }

    return loadRecaptcha(siteKey).then(function (grecaptcha) {
      return new Promise(function (resolve) {
        grecaptcha.ready(function () {
          grecaptcha.execute(siteKey, { action: 'submit' }).then(resolve, function () {
            resolve('');
          });
        });
      });
    }, function (error) {
      /* An unreachable reCAPTCHA must not strand the visitor: submit without a
       * token and let the server decide what that is worth. */
      console.warn('[gophercrm] reCAPTCHA unavailable', error);
      return '';
    });
  }

  function loadRecaptcha(siteKey) {
    if (recaptchaPromise) {
      return recaptchaPromise;
    }

    recaptchaPromise = new Promise(function (resolve, reject) {
      if (window.grecaptcha && window.grecaptcha.execute) {
        resolve(window.grecaptcha);
        return;
      }

      /* Several forms on one page share one reCAPTCHA script tag. */
      var tag = document.getElementById(RECAPTCHA_SCRIPT_ID);
      if (!tag) {
        tag = document.createElement('script');
        tag.id = RECAPTCHA_SCRIPT_ID;
        tag.src = RECAPTCHA_SRC + encodeURIComponent(siteKey);
        tag.async = true;
        document.head.appendChild(tag);
      }

      tag.addEventListener('load', function () {
        if (window.grecaptcha) {
          resolve(window.grecaptcha);
        } else {
          reject(new Error('reCAPTCHA loaded without its API'));
        }
      });
      tag.addEventListener('error', function () {
        reject(new Error('reCAPTCHA could not be loaded'));
      });
    });

    return recaptchaPromise;
  }

  /* ---------------------------------------------------------------- fetch */

  /* Both API calls answer in the {success, data, error} envelope, so unwrapping
   * happens once here. Credentials are omitted deliberately: these endpoints
   * are anonymous and the visitor's cookies for this site are none of our
   * business. */
  function fetchJSON(url, body) {
    var options = { method: body ? 'POST' : 'GET', credentials: 'omit' };
    if (body) {
      options.headers = { 'Content-Type': 'application/json' };
      options.body = JSON.stringify(body);
    }

    return fetch(url, options).then(function (response) {
      return response.text().then(function (text) {
        var parsed = null;
        try {
          parsed = text ? JSON.parse(text) : null;
        } catch (error) {
          parsed = null;
        }
        return {
          status: response.status,
          body: parsed,
          data: parsed && parsed.data ? parsed.data : null
        };
      });
    });
  }
})();
