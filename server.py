#!/usr/bin/env python3
import os
import imaplib
from flask import Flask, request, render_template, redirect, url_for, session, flash
import secrets

app = Flask(__name__)
app.secret_key = os.environ.get('SECRET_KEY', secrets.token_urlsafe(32))

IMAP_HOST = "imap.gmail.com"
IMAP_PORT = 993
FOLDER_ALL = "[Gmail]/All Mail"
FOLDER_TRASH = "[Gmail]/Trash"
FOLDER_SPAM = "[Gmail]/Spam"

DEFAULT_CONFIG = {
    "older_years": 1,
    "larger_mb": 10,
    "delete_spam": True,
    "delete_trash": False,
    "protect_starred": True,
    "categories": ["promotions", "updates", "social", "forums"]
}

def connect_imap(email_addr, app_pwd):
    conn = imaplib.IMAP4_SSL(IMAP_HOST, IMAP_PORT)
    conn.login(email_addr, app_pwd)
    return conn

def search_uids(conn, folder, query, max_per_rule=5000, readonly=True):
    if readonly:
        conn.select(folder, readonly=True)
    else:
        conn.select(folder, readonly=False)
    if query == "ALL":
        typ, data = conn.uid("search", None, "ALL")
    else:
        typ, data = conn.uid("search", None, f'(X-GM-RAW "{query}")')
    if typ != "OK" or not data or not data[0]:
        return []
    uids = [u.decode() for u in data[0].split()]
    return uids[:max_per_rule]

def build_queries(config):
    queries = []
    if config['delete_spam']:
        queries.append(("Spam", "in:spam"))
    if config['delete_trash']:
        queries.append(("Trash", "in:trash"))
    if config['older_years'] > 0:
        q = f"older_than:{config['older_years']}y -in:sent -in:chats -in:drafts"
        if config['protect_starred']:
            q += " -is:starred"
        queries.append((f"Older than {config['older_years']}y", q))
    if config['larger_mb'] > 0:
        q = f"has:attachment larger:{config['larger_mb']}m -in:sent -in:chats -in:drafts"
        if config['protect_starred']:
            q += " -is:starred"
        queries.append((f"Larger than {config['larger_mb']}MB", q))
    for cat in config['categories']:
        if not cat:
            continue
        q = f"category:{cat}"
        if config['protect_starred']:
            q += " -is:starred"
        queries.append((f"Category {cat}", q))
    return queries

def get_preview():
    if 'email' not in session or 'app_pwd' not in session:
        return []
    config = session.get('config', DEFAULT_CONFIG)
    conn = connect_imap(session['email'], session['app_pwd'])
    preview = []
    try:
        for rule, query in build_queries(config):
            folder = "[Gmail]/All Mail"
            if rule == "Spam":
                folder = FOLDER_SPAM
            if rule == "Trash":
                folder = FOLDER_TRASH
            uids = search_uids(conn, folder, query, readonly=True)
            preview.append({"rule": rule, "query": query, "count": len(uids)})
    finally:
        try:
            conn.logout()
        except:
            pass
    return preview

def do_execute():
    if 'email' not in session or 'app_pwd' not in session:
        return 0
    config = session.get('config', DEFAULT_CONFIG)
    conn = connect_imap(session['email'], session['app_pwd'])
    total = 0
    try:
        for rule, query in build_queries(config):
            folder = FOLDER_ALL
            if rule == "Spam":
                folder = FOLDER_SPAM
            if rule == "Trash":
                folder = FOLDER_TRASH
            uids = search_uids(conn, folder, query, readonly=False)
            if not uids:
                continue
            batch = list(uids)
            for chunk_start in range(0, len(batch), 200):
                chunk = batch[chunk_start:chunk_start + 200]
                try:
                    conn.uid("store", ",".join(chunk), "+FLAGS", "(\\Deleted)")
                    conn.expunge()
                except:
                    pass
            total += len(batch)
        if config.get('delete_trash'):
            try:
                uids_t = search_uids(conn, FOLDER_TRASH, "ALL", readonly=False)
                batch = list(uids_t)
                for chunk_start in range(0, len(batch), 200):
                    chunk = batch[chunk_start:chunk_start + 200]
                    try:
                        conn.uid("store", ",".join(chunk), "+FLAGS", "(\\Deleted)")
                        conn.expunge()
                    except:
                        pass
                total += len(batch)
            except:
                pass
    finally:
        try:
            conn.logout()
        except:
            pass
    return total

@app.route('/')
def index():
    if 'email' not in session:
        return redirect(url_for('login'))
    preview = session.get('preview', [])
    return render_template('index.html', email=session['email'], config=session.get('config', DEFAULT_CONFIG), preview=preview)

@app.route('/login', methods=['GET', 'POST'])
def login():
    if request.method == 'POST':
        email_addr = request.form.get('email', '').strip()
        app_pwd = request.form.get('password', '').strip()
        if not email_addr or not app_pwd:
            flash('Email and App Password required', 'danger')
            return render_template('login.html')
        try:
            conn = connect_imap(email_addr, app_pwd)
            conn.logout()
            session['email'] = email_addr
            session['app_pwd'] = app_pwd
            session['config'] = DEFAULT_CONFIG.copy()
            session['preview'] = []
            flash('Connected successfully', 'success')
            return redirect(url_for('index'))
        except Exception as e:
            flash(f'Login failed: {e}', 'danger')
    return render_template('login.html')

@app.route('/logout')
def logout():
    session.clear()
    flash('Logged out', 'info')
    return redirect(url_for('login'))

@app.route('/config', methods=['POST'])
def config_route():
    if 'email' not in session:
        return redirect(url_for('login'))
    cfg = session.get('config', DEFAULT_CONFIG.copy())
    try:
        cfg['older_years'] = int(request.form.get('older_years', cfg['older_years']))
        cfg['larger_mb'] = int(request.form.get('larger_mb', cfg['larger_mb']))
        cfg['protect_starred'] = request.form.get('protect_starred') == '1'
        cfg['delete_spam'] = request.form.get('delete_spam') == '1'
        cfg['delete_trash'] = request.form.get('delete_trash') == '1'
    except:
        pass
    session['config'] = cfg
    session['preview'] = []
    flash('Settings saved', 'success')
    return redirect(url_for('index'))

@app.route('/preview', methods=['POST'])
def preview_route():
    if 'email' not in session:
        return redirect(url_for('login'))
    try:
        prev = get_preview()
        session['preview'] = prev
        total = sum(p['count'] for p in prev)
        flash(f'Preview ready: {total} emails matched (dry-run)', 'info')
    except Exception as e:
        flash(f'Preview failed: {e}', 'danger')
    return redirect(url_for('index'))

@app.route('/execute', methods=['POST'])
def execute_route():
    if 'email' not in session:
        return redirect(url_for('login'))
    try:
        total = do_execute()
        session['preview'] = []
        flash(f'Deleted/purged {total} emails', 'warning')
    except Exception as e:
        flash(f'Execute failed: {e}', 'danger')
    return redirect(url_for('index'))

if __name__ == '__main__':
    app.run(host='0.0.0.0', port=int(os.environ.get('PORT', 5000)))
