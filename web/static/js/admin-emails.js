// The e-mail admin (/admin/emails). Three small things:
//  - outbox actions: buttons with data-outbox-action post to the admin API
//    and reload the page, or say why it did not work;
//  - data-confirm on a submit button asks before the form goes;
//  - the template editor warns before leaving with unsaved text.
(() => {
    const status = document.getElementById('outbox-status');
    const say = (ok, text) => {
        if (!status) return;
        status.textContent = text;
        status.className = 'mb-4 rounded-md p-3 text-sm ' + (ok ? 'bg-green-50 text-green-800' : 'bg-red-50 text-red-800');
    };

    for (const button of document.querySelectorAll('[data-outbox-action]')) {
        button.addEventListener('click', async () => {
            if (button.dataset.confirm && !confirm(button.dataset.confirm)) return;
            button.disabled = true;
            try {
                const response = await fetch('/api/admin/email/' + button.dataset.outboxAction, {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({id: Number(button.dataset.id)}),
                });
                const data = await response.json().catch(() => ({}));
                if (response.ok && data.success) {
                    say(true, data.message || 'Hotovo');
                    setTimeout(() => location.reload(), 800);
                    return;
                }
                say(false, data.error || ('Chyba ' + response.status));
            } catch (err) {
                say(false, 'Chyba: ' + err.message);
            }
            button.disabled = false;
        });
    }

    const form = document.querySelector('[data-template-form]');
    if (!form) return;
    // A preview of unsaved text comes back with that text in the form.
    let dirty = form.hasAttribute('data-unsaved');
    form.addEventListener('input', () => { dirty = true; });
    form.addEventListener('submit', event => {
        const button = event.submitter;
        if (button && button.dataset.confirm && !confirm(button.dataset.confirm)) {
            event.preventDefault();
            return;
        }
        dirty = false;
    });
    window.addEventListener('beforeunload', event => {
        if (dirty) event.preventDefault();
    });
})();
