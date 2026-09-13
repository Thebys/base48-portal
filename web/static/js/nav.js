/*
 * Navigation behaviour — Base48 Member Portal
 *
 * Loaded synchronously immediately after </nav>, not deferred: the nav is
 * already parsed at that point, so the active link is marked before the first
 * paint and nothing flashes. Nothing here waits for DOMContentLoaded.
 *
 * The theme is *applied* earlier still, by the inline bootstrap in <head> —
 * see layout.html. This file only handles changing it.
 */
(function () {
    'use strict';

    var root = document.documentElement;

    /* ---------------------------------------------------------------- theme */

    var THEME_KEY = 'base48-theme';           // 'system' | 'light' | 'dark'
    var media = window.matchMedia('(prefers-color-scheme: dark)');

    function readPref() {
        try {
            var v = localStorage.getItem(THEME_KEY);
            return v === 'light' || v === 'dark' ? v : 'system';
        } catch (e) {
            // Private mode, or storage blocked. Follow the OS and move on.
            return 'system';
        }
    }

    function paint(pref) {
        root.classList.toggle('dark', pref === 'dark' || (pref === 'system' && media.matches));
    }

    function syncButtons(pref) {
        var buttons = document.querySelectorAll('[data-theme-switch] button[data-theme]');
        for (var i = 0; i < buttons.length; i++) {
            buttons[i].setAttribute('aria-pressed', buttons[i].dataset.theme === pref ? 'true' : 'false');
        }
    }

    function setTheme(pref) {
        try {
            if (pref === 'system') {
                localStorage.removeItem(THEME_KEY);
            } else {
                localStorage.setItem(THEME_KEY, pref);
            }
        } catch (e) { /* the choice just won't outlive the tab */ }
        paint(pref);
        syncButtons(pref);
    }

    document.addEventListener('click', function (e) {
        var btn = e.target.closest('[data-theme-switch] button[data-theme]');
        if (btn) setTheme(btn.dataset.theme);
    });

    // Only meaningful while the preference is 'system'.
    media.addEventListener('change', function () {
        if (readPref() === 'system') paint('system');
    });

    // Keep every open tab in agreement.
    window.addEventListener('storage', function (e) {
        if (e.key !== THEME_KEY && e.key !== null) return;
        var pref = readPref();
        paint(pref);
        syncButtons(pref);
    });

    syncButtons(readPref());

    /* ---------------------------------------------------------------- menus */

    var menus = [];

    function closeAll(except) {
        menus.forEach(function (m) { if (m !== except) m.close(); });
    }

    document.querySelectorAll('[data-menu]').forEach(function (wrap) {
        var button = wrap.querySelector('[data-menu-button]');
        var panel = wrap.querySelector('[data-menu-panel]');
        if (!button || !panel) return;

        var menu = {
            isOpen: function () { return !panel.hidden; },
            open: function () {
                closeAll(menu);
                panel.hidden = false;
                button.setAttribute('aria-expanded', 'true');
            },
            close: function () {
                if (panel.hidden) return;
                panel.hidden = true;
                button.setAttribute('aria-expanded', 'false');
            }
        };
        menus.push(menu);

        function items() {
            return Array.prototype.filter.call(
                panel.querySelectorAll('a[href], button:not([disabled])'),
                function (el) { return el.offsetParent !== null; }
            );
        }

        function focusAt(index) {
            var list = items();
            if (!list.length) return;
            // Wrap around at both ends.
            list[(index + list.length) % list.length].focus();
        }

        button.addEventListener('click', function (e) {
            e.preventDefault();
            menu.isOpen() ? menu.close() : menu.open();
        });

        button.addEventListener('keydown', function (e) {
            if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
                e.preventDefault();
                menu.open();
                focusAt(e.key === 'ArrowDown' ? 0 : -1);
            }
        });

        panel.addEventListener('keydown', function (e) {
            if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
            e.preventDefault();
            var list = items();
            var at = list.indexOf(document.activeElement);
            focusAt(at + (e.key === 'ArrowDown' ? 1 : -1));
        });

        // A link inside the panel navigates away; a theme button must not close
        // the panel, so the user can compare the two themes in place.
        panel.addEventListener('click', function (e) {
            if (e.target.closest('a[href]')) menu.close();
        });

        wrap.addEventListener('focusout', function (e) {
            if (!wrap.contains(e.relatedTarget)) menu.close();
        });
    });

    document.addEventListener('keydown', function (e) {
        if (e.key !== 'Escape') return;
        var open = menus.filter(function (m) { return m.isOpen(); });
        if (!open.length) return;
        var wrap = document.activeElement && document.activeElement.closest('[data-menu]');
        closeAll();
        // Escape returns focus to the trigger, per the menu-button pattern.
        if (wrap) wrap.querySelector('[data-menu-button]').focus();
    });

    document.addEventListener('click', function (e) {
        if (!e.target.closest('[data-menu]')) closeAll();
    });

    /* --------------------------------------------------------- mobile panel */

    var burger = document.querySelector('[data-mobile-toggle]');
    var mobile = document.querySelector('[data-mobile-panel]');
    if (burger && mobile) {
        burger.addEventListener('click', function () {
            mobile.hidden = !mobile.hidden;
            burger.setAttribute('aria-expanded', mobile.hidden ? 'false' : 'true');
        });
    }

    /* ----------------------------------------------------------- active link */

    // Longest matching href wins, so /admin/bar/cards lights up "Bar" rather
    // than also lighting up every shorter prefix. "/" only matches exactly.
    var path = location.pathname.replace(/\/+$/, '') || '/';
    var best = null;
    var bestLen = 0;

    document.querySelectorAll('[data-nav-links] a[href^="/"]').forEach(function (a) {
        var href = a.getAttribute('href').replace(/\/+$/, '') || '/';
        var hit = href === '/' ? path === '/' : (path === href || path.indexOf(href + '/') === 0);
        if (hit && href.length >= bestLen) {
            bestLen = href.length;
            best = href;
        }
    });

    if (best !== null) {
        document.querySelectorAll('[data-nav-links] a[href^="/"]').forEach(function (a) {
            var href = a.getAttribute('href').replace(/\/+$/, '') || '/';
            if (href === best) a.setAttribute('aria-current', 'page');
        });
    }
}());
