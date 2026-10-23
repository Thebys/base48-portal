/*
 * Putting data into markup — Base48 Member Portal
 *
 * Everything that comes from the database is text, not markup: names,
 * usernames and e-mails people register in Keycloak, notes, project texts,
 * bank data. A page that builds an HTML string for innerHTML passes every
 * such value through esc(). Where possible, build elements and set
 * textContent instead; then there is nothing to escape.
 *
 * Loaded by layout.html before the page content, so page scripts can use it.
 */

// esc(value) is value as text that is safe anywhere in HTML: element
// content and quoted attribute values.
function esc(value) {
    return String(value ?? '')
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

// jsArg(value) is value as a JS string literal inside an inline handler:
//   onclick="removeVS(${project.id}, ${jsArg(vs)})"
// Escaping quotes with a backslash is not enough there: the browser decodes
// entities in the attribute first, so "&#39;" in a name becomes a quote that
// ends the string. JSON.stringify makes a valid JS string, esc() keeps it
// inside the attribute, and the browser undoes only the esc().
function jsArg(value) {
    return esc(JSON.stringify(String(value ?? '')));
}
