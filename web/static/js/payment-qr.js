// The period buttons under the profile's payment QR (partials/payment_qr.html):
// a click shows the QR for that amount instead of opening it on its own.
(() => {
    for (const box of document.querySelectorAll('[data-payment-qr]')) {
        if (box.dataset.ready) continue;
        box.dataset.ready = 'true';
        const image = box.querySelector('[data-qr-image]');
        const amount = box.querySelector('[data-qr-amount] strong');
        const note = box.querySelector('[data-qr-note]');
        const options = box.querySelectorAll('[data-qr-option]');
        for (const option of options) {
            option.addEventListener('click', event => {
                event.preventDefault();
                image.src = option.dataset.url;
                if (amount) amount.textContent = option.dataset.amount + ' Kč';
                if (note) note.textContent = '(' + option.dataset.note + ')';
                for (const o of options) {
                    const on = o === option;
                    o.setAttribute('aria-pressed', String(on));
                    o.classList.toggle('btn-primary', on);
                    o.classList.toggle('btn-secondary', !on);
                }
            });
        }
    }
})();
