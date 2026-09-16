/* Workshop reservation history — the collapsible "this and last month" table.
 *
 * Members (/workshop) and admins (/admin/workshop) get the exact same listing,
 * so it is rendered from here instead of being copy-pasted into both templates.
 * The rows come from the `history` array of /api/member/workshop; the endpoint
 * already limits them to the window, this file only draws them.
 *
 * Usage: put <div id="workshop-history"></div> in the page, then call
 *   renderWorkshopHistory(document.getElementById('workshop-history'), data)
 * with the API payload. Safe to call on every reload — the card is built once
 * and keeps its open/closed state.
 */
(function (global) {
    'use strict';

    var COLUMNS = ['Stání', 'Kdo', 'Kdy', 'Poznámka'];

    function el(tag, cls, text) {
        var e = document.createElement(tag);
        if (cls) e.className = cls;
        if (text !== undefined) e.textContent = text;
        return e;
    }

    function parseTs(s) { return new Date(s.replace(' ', 'T')); }

    // 1 rezervace / 2-4 rezervace / 5+ rezervací
    function fmtCount(n) {
        if (n === 1) return '1 rezervace';
        if (n >= 2 && n <= 4) return n + ' rezervace';
        return n + ' rezervací';
    }

    function fmtDays(n) {
        if (n === 1) return '1 den';
        if (n >= 2 && n <= 4) return n + ' dny';
        return n + ' dní';
    }

    // "14. 9. 10:00 – 18:00" inside one day, "14. 9. 10:00 – 15. 9. 8:00" across days.
    function fmtRange(startsAt, endsAt) {
        var a = parseTs(startsAt);
        var b = parseTs(endsAt);
        var date = { day: 'numeric', month: 'numeric' };
        var time = { hour: '2-digit', minute: '2-digit' };
        var from = a.toLocaleDateString('cs-CZ', date) + ' ' + a.toLocaleTimeString('cs-CZ', time);
        var sameDay = a.toDateString() === b.toDateString();
        var to = (sameDay ? '' : b.toLocaleDateString('cs-CZ', date) + ' ') + b.toLocaleTimeString('cs-CZ', time);
        return from + ' – ' + to;
    }

    function monthLabel(d) {
        var label = d.toLocaleDateString('cs-CZ', { month: 'long', year: 'numeric' });
        return label.charAt(0).toUpperCase() + label.slice(1);
    }

    // Every calendar day a reservation touches counts, so an overnight session
    // shows as the two days the bay was really occupied. An end at midnight
    // belongs to the day before, hence the -1 ms.
    function collectDays(entry, into) {
        var day = parseTs(entry.starts_at);
        day.setHours(0, 0, 0, 0);
        var last = parseTs(entry.ends_at).getTime() - 1;
        while (day.getTime() <= last) {
            into[day.getFullYear() + '-' + day.getMonth() + '-' + day.getDate()] = true;
            day.setDate(day.getDate() + 1);
        }
    }

    // Reservations arrive newest first, so a month break is just a change of
    // month between two neighbours.
    function groupByMonth(history) {
        var groups = [];
        history.forEach(function (entry) {
            var start = parseTs(entry.starts_at);
            var key = start.getFullYear() + '-' + start.getMonth();
            var group = groups[groups.length - 1];
            if (!group || group.key !== key) {
                group = { key: key, label: monthLabel(start), entries: [] };
                groups.push(group);
            }
            group.entries.push(entry);
        });
        return groups;
    }

    // Builds the card on first render; later renders reuse it so an open
    // <details> does not snap shut under the member every time the page reloads.
    function ensureCard(container) {
        var card = container.querySelector('details');
        if (card) return card;

        card = el('details', 'bg-white border border-gray-200 rounded-lg shadow-sm');

        var summary = el('summary', 'px-5 py-4 cursor-pointer select-none');
        summary.appendChild(el('h2', 'text-lg font-semibold text-gray-900 inline', 'Historie rezervací'));
        summary.appendChild(el('span', 'text-sm text-gray-500 ml-2', 'tento a minulý měsíc'));
        summary.appendChild(el('span', 'js-history-count text-sm text-gray-400 ml-2'));
        card.appendChild(summary);

        var body = el('div', 'border-t border-gray-200 px-5 py-4');

        var scroll = el('div', 'overflow-x-auto');
        var table = el('table', 'min-w-full text-sm');
        var thead = el('thead');
        var headRow = el('tr', 'text-left text-xs font-medium text-gray-500 uppercase tracking-wider border-b border-gray-200');
        COLUMNS.forEach(function (label) {
            headRow.appendChild(el('th', 'py-2 pr-4 whitespace-nowrap', label));
        });
        thead.appendChild(headRow);
        table.appendChild(thead);
        table.appendChild(el('tbody', 'js-history-body'));
        scroll.appendChild(table);
        body.appendChild(scroll);

        body.appendChild(el('p', 'js-history-empty hidden text-sm text-gray-400',
            'Za tento ani minulý měsíc tu není žádná dokončená rezervace.'));
        card.appendChild(body);

        container.appendChild(card);
        return card;
    }

    // "Září 2026  ·  4 rezervace · 3 dny · 27 h" — how much the bays were used.
    function monthRow(group) {
        var days = {};
        var hours = 0;
        group.entries.forEach(function (entry) {
            collectDays(entry, days);
            hours += (parseTs(entry.ends_at) - parseTs(entry.starts_at)) / 3600000;
        });

        var tr = el('tr');
        var td = el('td', 'pt-4 pb-1 whitespace-nowrap');
        td.colSpan = COLUMNS.length;
        td.appendChild(el('span', 'font-semibold text-gray-700', group.label));
        td.appendChild(el('span', 'text-gray-400 ml-3',
            fmtCount(group.entries.length) + ' · ' + fmtDays(Object.keys(days).length) + ' · ' + Math.round(hours) + ' h'));
        tr.appendChild(td);
        return tr;
    }

    function historyRow(entry, resources) {
        var resource = resources.find(function (res) { return res.id === entry.resource_id; });

        var tr = el('tr', 'border-b border-gray-100');
        tr.appendChild(el('td', 'py-1.5 pr-4 text-gray-500 whitespace-nowrap',
            resource ? resource.name : String(entry.resource_id)));

        var who = el('td', 'py-1.5 pr-4 whitespace-nowrap font-medium ' + (entry.mine ? 'text-green-700' : 'text-gray-900'));
        who.textContent = entry.username + (entry.mine ? ' (já)' : '');
        // A bumped reservation was cut short by an admin — the range below is
        // what was booked, not what was used, so say so.
        if (entry.state === 'bumped') {
            who.appendChild(el('span', 'badge badge-orange ml-2', 'ukončeno adminem'));
        }
        tr.appendChild(who);

        tr.appendChild(el('td', 'py-1.5 pr-4 text-gray-500 whitespace-nowrap', fmtRange(entry.starts_at, entry.ends_at)));
        tr.appendChild(el('td', 'py-1.5 text-gray-400 italic whitespace-nowrap', entry.note || '—'));
        return tr;
    }

    // container: the element to render into. data: the /api/member/workshop payload.
    global.renderWorkshopHistory = function (container, data) {
        if (!container) return;

        var history = (data && data.history) || [];
        var resources = (data && data.resources) || [];

        var card = ensureCard(container);
        var tbody = card.querySelector('.js-history-body');

        card.querySelector('.js-history-count').textContent = fmtCount(history.length);

        tbody.innerHTML = '';
        groupByMonth(history).forEach(function (group) {
            tbody.appendChild(monthRow(group));
            group.entries.forEach(function (entry) { tbody.appendChild(historyRow(entry, resources)); });
        });

        card.querySelector('table').hidden = history.length === 0;
        card.querySelector('.js-history-empty').classList.toggle('hidden', history.length > 0);
    };
})(window);
