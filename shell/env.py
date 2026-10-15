"""运行时配置。

所有敏感信息一律从环境变量读取，不在仓库中保留任何明文密钥。
缺少【必需】变量时直接抛异常退出，避免带着空密钥静默启动。
变量的完整清单见仓库根目录的 .env.example。
"""

import json
import os
import re


def _required_str(name):
    value = os.getenv(name)
    if value is None or value.strip() == '':
        raise RuntimeError(f'environment variable {name} is required but not set')
    return value


def _optional_str(name, default):
    value = os.getenv(name)
    if value is None or value.strip() == '':
        return default
    return value


def _required_int(name):
    value = _required_str(name)
    try:
        return int(value)
    except ValueError as e:
        raise RuntimeError(f'environment variable {name} must be an integer, got: {value!r}') from e


def _optional_int(name, default):
    value = os.getenv(name)
    if value is None or value.strip() == '':
        return default
    try:
        return int(value)
    except ValueError as e:
        raise RuntimeError(f'environment variable {name} must be an integer, got: {value!r}') from e


def _required_list(name):
    """逗号分隔的列表，忽略空项。"""
    return [item.strip() for item in _required_str(name).split(',') if item.strip() != '']


def _required_regex_list(name):
    """JSON 数组形式的正则列表，逐条预编译校验。

    列表类值不能用逗号分隔：正则里本来就可能出现逗号（例如量词 {m,n}），
    用 JSON 数组解析才无歧义，也不需要为正则本身做额外转义。
    """
    value = _required_str(name)
    try:
        patterns = json.loads(value)
    except json.JSONDecodeError as e:
        raise RuntimeError(
            f'environment variable {name} must be a JSON array of regex strings, got: {value!r} '
            f'({e.msg} at position {e.pos})') from e
    if not isinstance(patterns, list) or not all(isinstance(p, str) for p in patterns):
        raise RuntimeError(
            f'environment variable {name} must be a JSON array of regex strings, got: {value!r}')
    if any(p.strip() == '' for p in patterns):
        raise RuntimeError(f'environment variable {name} must not contain an empty regex: {value!r}')
    for pattern in patterns:
        try:
            re.compile(pattern)
        except re.error as e:
            raise RuntimeError(f'invalid regex in environment variable {name}: {pattern!r} ({e})') from e
    return patterns


AUTH_USERNAME = _required_str('AUTH_USERNAME')
AUTH_PASSWORD = _required_str('AUTH_PASSWORD')
AUTH_TOKEN = _required_str('AUTH_TOKEN')

TELEGRAM_BOT_TOKEN = _required_str('TELEGRAM_BOT_TOKEN')
TELEGRAM_CHAT_ID = _required_int('TELEGRAM_CHAT_ID')

BARK_GET_URL = _optional_str('BARK_GET_URL', 'https://api.day.app/')
BARK_POST_URL = _optional_str('BARK_POST_URL', 'https://api.day.app/push')
BARK_URL_LIST = _required_list('BARK_URL_LIST')
BARK_DEVICE_KEY_LIST = _required_list('BARK_DEVICE_KEY_LIST')

AES_128_ECB_CONFIG = {
    'key': _required_str('AES_128_ECB_KEY').encode(),
    'iv': _required_str('AES_128_ECB_IV').encode()
}

EMAIL_DELIVER_CONFIG = {
    'sender_email': _required_str('EMAIL_DELIVER_SENDER_EMAIL'),
    'receiver_email': _required_str('EMAIL_DELIVER_RECEIVER_EMAIL'),
    'password': _required_str('EMAIL_DELIVER_PASSWORD'),
    'smtp_server': _optional_str('EMAIL_DELIVER_SMTP_SERVER', 'smtp.exmail.qq.com'),
    'smtp_port': _optional_int('EMAIL_DELIVER_SMTP_PORT', 465)
}

EMAIL_TRIGGER_CONFIG = {
    'login_email': _required_str('EMAIL_TRIGGER_LOGIN_EMAIL'),
    'password': _required_str('EMAIL_TRIGGER_PASSWORD'),
    'imap_server': _optional_str('EMAIL_TRIGGER_IMAP_SERVER', 'imap.exmail.qq.com'),
    'imap_port': _optional_int('EMAIL_TRIGGER_IMAP_PORT', 993)
}

# 过滤/识别规则，均为 JSON 数组形式的正则字符串，部署时按需调整。
REGEX_FILTER_BLACK = _required_regex_list('REGEX_FILTER_BLACK')
REGEX_FILTER_WHITE = _required_regex_list('REGEX_FILTER_WHITE')
REGEX_CAPTCHA = _required_regex_list('REGEX_CAPTCHA')
REGEX_EMERGENCY = _required_regex_list('REGEX_EMERGENCY')
