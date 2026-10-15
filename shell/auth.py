from functools import wraps

from flask import jsonify, request

import env
import utils


def check_auth(username, password):
    """验证用户名和密码"""
    return username == env.AUTH_USERNAME and password == env.AUTH_PASSWORD


def authenticate_login():
    """发送 401 响应以触发基本身份验证"""
    utils.print_with_time("login authentication failed")
    return jsonify({"message": "Authentication required"}), 401, {'WWW-Authenticate': 'Basic realm="Login Required"'}


def requires_login(f):
    @wraps(f)
    def decorated(*args, **kwargs):
        auth = request.authorization
        if not auth or not check_auth(auth.username, auth.password):
            return authenticate_login()
        return f(*args, **kwargs)

    return decorated


def check_token(token):
    """验证令牌"""
    return token == env.AUTH_TOKEN


def authenticate_token():
    """发送 401 响应以触发令牌验证"""
    utils.print_with_time("token authentication failed")
    return jsonify({"message": "Authentication required"}), 401


def requires_token(f):
    @wraps(f)
    def decorated(*args, **kwargs):
        token = request.headers.get('Authorization')
        if token:
            parts = token.split(" ")
            if len(parts) > 1 and check_token(parts[1]):
                return f(*args, **kwargs)
        token = request.args.get('token')
        if token and check_token(token):
            return f(*args, **kwargs)
        return authenticate_token()

    return decorated
