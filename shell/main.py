# dists: flask, requests, python-telegram-bot, Werkzeug, pycryptodome
import asyncio
import os

from flask import Flask, request, jsonify
from werkzeug.middleware.proxy_fix import ProxyFix

import auth
import deliver
import model
import process
import response
import trigger
import utils

app = Flask(__name__)
app.wsgi_app = ProxyFix(app.wsgi_app, x_for=1, x_proto=1, x_host=1, x_port=1)


@app.before_request
def log_request_info():
    utils.print_with_time(f'Request from {request.remote_addr}, {request.method}, {request.path}')


@app.route('/webhook', methods=['POST'])
@auth.requires_token
def receive_webhook():
    json_data = request.get_json()
    if not json_data:
        return jsonify(response.failed_response("invalid json")), 400
    msg = model.Message(**json_data)
    if not process.execute(msg):
        return jsonify(response.failed_response("process failed")), 200
    if not deliver.execute(msg):
        return jsonify(response.failed_response("deliver failed")), 200
    return jsonify(response.success_response()), 200


if __name__ == '__main__':
    if utils.str_to_bool(os.getenv('RUN_TRIGGER')):
        asyncio.run(trigger.execute())
    else:
        app.run(host='0.0.0.0', port=8889)
