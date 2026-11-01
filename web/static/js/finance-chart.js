// The finance history chart (partials/finance.html): closed months, the
// accounts' balance (average, and its range in the month), members' debt,
// what members paid and what they were charged, on one axis in crowns. The
// legend turns each of them off and on; the axis follows what is shown.
//
// SVG, so the theme's colour tokens reach the marks in both modes. Every text
// is set with textContent. Without JavaScript the monthly table carries the
// same numbers.
(() => {
    const chart = document.getElementById('fin-chart');
    const source = document.getElementById('fin-history');
    if (!chart || !source) return;
    const card = chart.closest('.fin-card');
    const all = JSON.parse(source.textContent || 'null') || [];
    if (!all.length) {
        card.hidden = true;
        return;
    }

    const NS = 'http://www.w3.org/2000/svg';
    const MONTHS = ['led', 'úno', 'bře', 'dub', 'kvě', 'čvn', 'čvc', 'srp', 'zář', 'říj', 'lis', 'pro'];
    const MONTH_NAMES = ['leden', 'únor', 'březen', 'duben', 'květen', 'červen', 'červenec', 'srpen', 'září', 'říjen', 'listopad', 'prosinec'];

    const kc = v => Math.round(v).toLocaleString('cs-CZ') + ' Kč';
    const monthName = m => MONTH_NAMES[+m.slice(5, 7) - 1] + ' ' + m.slice(0, 4);
    const members = n => n === 1 ? '1 člen' : n >= 2 && n <= 4 ? n + ' členové' : n + ' členů';

    function svgEl(tag, attrs, parent) {
        const el = document.createElementNS(NS, tag);
        for (const k in attrs) el.setAttribute(k, attrs[k]);
        if (parent) parent.appendChild(el);
        return el;
    }
    function htmlEl(tag, cls, text, parent) {
        const el = document.createElement(tag);
        if (cls) el.className = cls;
        if (text != null) el.textContent = text;
        if (parent) parent.appendChild(el);
        return el;
    }

    // Round gridlines: 1, 2, 2.5 or 5 times a power of ten, about four of them.
    function scale(max) {
        if (max <= 0) return { top: 1000, step: 250 };
        const raw = max / 4;
        const p = Math.pow(10, Math.floor(Math.log10(raw)));
        const n = raw / p;
        const step = (n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10) * p;
        return { top: Math.ceil(max / step) * step, step };
    }

    // Runs of consecutive months that have a balance: the band and the line
    // break where the bank has not told us anything.
    function runs(data) {
        const out = [];
        let run = [];
        data.forEach((d, i) => {
            if (d.balance) run.push(i);
            else if (run.length) { out.push(run); run = []; }
        });
        if (run.length) out.push(run);
        return out;
    }

    const tip = htmlEl('div', 'fin-tip', null, chart);
    tip.hidden = true;

    let months = 36;
    const buttons = card.querySelectorAll('.fin-range button');
    for (const button of buttons) {
        button.addEventListener('click', () => {
            months = Number(button.dataset.months);
            for (const b of buttons) b.setAttribute('aria-pressed', String(b === button));
            draw();
        });
    }

    // What each series draws, in the legend's order, which is also the order
    // of the end labels when they would collide.
    const SERIES = {
        balance: { value: d => d.balance && d.balance.avg, top: d => d.balance && d.balance.high },
        debt: { value: d => d.debt },
        paid: { value: d => d.paid },
        charged: { value: d => d.charged },
    };
    const shown = new Set(Object.keys(SERIES));
    for (const button of card.querySelectorAll('.fin-legend button')) {
        button.addEventListener('click', () => {
            const name = button.dataset.series;
            if (shown.has(name)) shown.delete(name);
            else shown.add(name);
            button.setAttribute('aria-pressed', String(shown.has(name)));
            draw();
        });
    }

    let drawnWidth = 0;
    new ResizeObserver(() => {
        if (chart.clientWidth !== drawnWidth) draw();
    }).observe(chart);

    function draw() {
        drawnWidth = chart.clientWidth;
        chart.querySelector('svg')?.remove();
        tip.hidden = true;
        const W = chart.clientWidth;
        if (!W) return;

        const data = months ? all.slice(-months) : all;
        const n = data.length;
        const narrow = W < 520;
        const H = narrow ? 240 : 300;
        const m = { t: 12, r: narrow ? 8 : 80, b: 28, l: 52 };
        const pw = W - m.l - m.r;
        const ph = H - m.t - m.b;
        const band = pw / n;
        const x = i => m.l + (i + 0.5) * band;
        const base = m.t + ph;

        let max = 0;
        for (const d of data) {
            for (const name of shown) max = Math.max(max, (SERIES[name].top || SERIES[name].value)(d) || 0);
        }
        const { top, step } = scale(max * 1.04);
        const y = v => base - Math.max(0, v) / top * ph;

        const svg = svgEl('svg', {
            width: W, height: H, viewBox: `0 0 ${W} ${H}`, role: 'img', tabindex: 0,
            'aria-label': `Po měsících od ${monthName(data[0].month)} do ${monthName(data[n - 1].month)}: ` +
                'zůstatek na účtech, dluh členů, zaplacené příspěvky a předpis. Čísla jsou i v měsíčním přehledu.',
        });
        chart.prepend(svg);

        // Ticks in full below 10 000, in thousands on an axis that reaches it.
        for (let v = 0; v <= top + 1e-9; v += step) {
            svgEl('line', { x1: m.l, x2: W - m.r, y1: y(v), y2: y(v), class: v === 0 ? 'axis' : 'grid' }, svg);
            svgEl('text', { x: m.l - 8, y: y(v) + 4, class: 'tick', 'text-anchor': 'end' }, svg).textContent =
                top < 10000 || v === 0 ? v.toLocaleString('cs-CZ') : (v / 1000).toLocaleString('cs-CZ') + ' tis.';
        }

        // Month names while they fit (January shows its year), else years.
        if (n <= 18) {
            const every = Math.max(1, Math.ceil(n * 36 / pw));
            data.forEach((d, i) => {
                if ((n - 1 - i) % every) return;
                const january = d.month.endsWith('-01');
                svgEl('text', { x: x(i), y: H - 8, class: january ? 'tick year' : 'tick', 'text-anchor': 'middle' }, svg)
                    .textContent = january ? d.month.slice(0, 4) : MONTHS[+d.month.slice(5, 7) - 1];
            });
        } else {
            const januaries = [];
            data.forEach((d, i) => { if (d.month.endsWith('-01')) januaries.push(i); });
            const every = Math.max(1, Math.ceil(januaries.length * 44 / pw));
            januaries.forEach((i, k) => {
                if ((januaries.length - 1 - k) % every) return;
                const x0 = x(i) - band / 2;
                svgEl('line', { x1: x0, x2: x0, y1: base, y2: base + 5, class: 'axis' }, svg);
                svgEl('text', { x: x0 + 3, y: H - 8, class: 'tick year' }, svg).textContent = data[i].month.slice(0, 4);
            });
        }

        // The balance's range behind everything, then the lines, the
        // reference first and the balance on top.
        const balanceRuns = runs(data);
        if (shown.has('balance')) {
            for (const run of balanceRuns) {
                const upper = run.map(i => `${x(i)},${y(data[i].balance.high)}`);
                const lower = run.slice().reverse().map(i => `${x(i)},${y(data[i].balance.low)}`);
                svgEl('path', { d: 'M' + upper.concat(lower).join('L') + 'Z', class: 'band' }, svg);
            }
        }
        const line = (name, indexes) => {
            const value = SERIES[name].value;
            svgEl('path', { d: 'M' + indexes.map(i => `${x(i)},${y(value(data[i]))}`).join('L'), class: 'ln ' + name }, svg);
        };
        const every = data.map((_, i) => i);
        for (const name of ['charged', 'paid', 'debt']) {
            if (shown.has(name)) line(name, every);
        }
        if (shown.has('balance')) {
            for (const run of balanceRuns) line('balance', run);
        }

        // The last month's values at the ends of the lines. A label that
        // would touch one placed before it is left to the tooltip.
        const placed = [];
        for (const name of Object.keys(SERIES)) {
            const v = SERIES[name].value(data[n - 1]);
            if (!shown.has(name) || v == null) continue;
            svgEl('circle', { cx: x(n - 1), cy: y(v), r: 4, class: 'dot ' + name }, svg);
            if (narrow || placed.some(p => Math.abs(p - y(v)) < 15)) continue;
            placed.push(y(v));
            svgEl('text', { x: x(n - 1) + 9, y: y(v) + 4, class: 'end' }, svg).textContent = kc(v);
        }

        // The crosshair snaps to a month and the tooltip lists all of it.
        const cross = svgEl('line', { y1: m.t, y2: base, class: 'cross', visibility: 'hidden' }, svg);
        const hit = svgEl('rect', { x: m.l, y: 0, width: pw, height: H, fill: 'transparent' }, svg);
        let at = n - 1;
        const row = (cls, value, name) => {
            const r = htmlEl('div', 'r', null, tip);
            htmlEl('i', 'key key-' + cls, null, r).setAttribute('aria-hidden', 'true');
            htmlEl('b', null, value, r);
            htmlEl('span', null, name, r);
        };
        const show = i => {
            at = Math.max(0, Math.min(n - 1, i));
            const d = data[at];
            cross.setAttribute('x1', x(at));
            cross.setAttribute('x2', x(at));
            cross.setAttribute('visibility', 'visible');

            tip.replaceChildren();
            htmlEl('div', 't', monthName(d.month), tip);
            if (shown.has('balance') && d.balance) {
                row('balance', kc(d.balance.avg), 'zůstatek průměrně');
                htmlEl('div', 'sub', `na konci ${kc(d.balance.end)}, v měsíci ${kc(d.balance.low)} až ${kc(d.balance.high)}`, tip);
            }
            if (shown.has('debt')) row('debt', kc(d.debt), `dluh (${members(d.debtors)})`);
            if (shown.has('paid')) row('paid', kc(d.paid), 'zaplaceno');
            if (shown.has('charged')) row('charged', kc(d.charged), 'předpis');
            tip.hidden = false;

            const tw = tip.offsetWidth;
            const left = x(at) + 12 + tw <= W ? x(at) + 12 : x(at) - 12 - tw;
            tip.style.left = Math.max(0, left) + 'px';
            tip.style.top = m.t + 'px';
        };
        const hide = () => {
            cross.setAttribute('visibility', 'hidden');
            tip.hidden = true;
        };
        const pointer = e => {
            const r = svg.getBoundingClientRect();
            show(Math.floor((e.clientX - r.left - m.l) / band));
        };
        hit.addEventListener('pointermove', pointer);
        hit.addEventListener('pointerdown', pointer);
        hit.addEventListener('pointerleave', hide);
        svg.addEventListener('focus', () => show(at));
        svg.addEventListener('blur', hide);
        svg.addEventListener('keydown', e => {
            const keys = { ArrowLeft: at - 1, ArrowRight: at + 1, Home: 0, End: n - 1 };
            if (e.key in keys) {
                show(keys[e.key]);
                e.preventDefault();
            }
        });
    }
})();
