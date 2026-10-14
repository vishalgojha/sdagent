#!/usr/bin/env python3
import os
import imaplib
import email
from email.header import decode_header
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

@app.route('/')
def index():
    if 'email' not in session:
        return redirect(url_for('login'))
    return render_template('index.html', email=session['email'], config=session.get('config', DEFAULT_CONFIG))

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
