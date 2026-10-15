import asyncio
import base64
import html
import json
import smtplib
from email.mime.multipart import MIMEMultipart
from email.mime.text import MIMEText
from urllib import parse

import requests
import telegram
from Crypto.Cipher import AES
from Crypto.Random import get_random_bytes
from Crypto.Util import Padding

import consts
import env
import model
import utils


def crypt_for_bark(content):
    key = env.AES_128_ECB_CONFIG['key']
    iv = env.AES_128_ECB_CONFIG['iv']
    cipher = AES.new(key, AES.MODE_CBC, iv)
    content = Padding.pad(content.encode(), AES.block_size)
    return iv.decode(), base64.b64encode(cipher.encrypt(content)).decode()


def gen_complex_text(msg: model.Message):
    text = ''
    if msg.msg_type == consts.ENUM_MSG_TYPE_CALL:
        text = (f'未接来电：{msg.title}\n\n'
                f'来电时间：{utils.get_date_time(msg.incoming_time)}\n')
    elif msg.msg_type == consts.ENUM_MSG_TYPE_TEXT:
        text = (f'{msg.content}\n\n短信来自: {msg.title}\n\n'
                f'发送时间：{utils.get_date_time(msg.transmit_time)}\n'
                f'转发时间：{utils.get_date_time(msg.receive_time)}\n')
    if msg.battery_level is not None:
        text += f'\n当前电量: {msg.battery_level}%\n'
    return text


async def send_to_bark(msg: model.Message):
    copy_content = msg.captcha_code
    title = ''
    content = ''
    if msg.msg_type == consts.ENUM_MSG_TYPE_CALL:
        title = f'New Call From {msg.title}'
        copy_content = msg.title
    elif msg.msg_type == consts.ENUM_MSG_TYPE_TEXT:
        title = f'New Message From {msg.title}'
        content = msg.content

    request_body = {'title': title, 'body': content, 'isArchive': 1}
    if copy_content is not None:
        request_body['autoCopy'] = 1
        request_body['copy'] = copy_content
    if msg.emergency is True:
        request_body['level'] = 'critical'
        request_body['volume'] = 1
    for bark_device_key in env.BARK_DEVICE_KEY_LIST:
        request_body['device_key'] = bark_device_key
        requests.post(env.BARK_POST_URL, json=request_body)
    # iv, cipher_text = crypt_for_bark(json.dumps(request_body))
    # for bark_url in env.BARK_URL_LIST:
    #     requests.post(bark_url, data={'ciphertext': cipher_text})


async def send_to_email(msg: model.Message):
    message = MIMEMultipart('alternative')
    text = gen_complex_text(msg)
    if msg.msg_type == consts.ENUM_MSG_TYPE_CALL:
        message['Subject'] = f'New Call From {msg.title}'
    elif msg.msg_type == consts.ENUM_MSG_TYPE_TEXT:
        message['Subject'] = f'New Message From {msg.title}'
    message['From'] = env.EMAIL_DELIVER_CONFIG['sender_email']
    message['To'] = env.EMAIL_DELIVER_CONFIG['receiver_email']

    part = MIMEText(text, 'plain')
    message.attach(part)

    with smtplib.SMTP_SSL(env.EMAIL_DELIVER_CONFIG['smtp_server'], env.EMAIL_DELIVER_CONFIG['smtp_port']) as server:
        server.login(env.EMAIL_DELIVER_CONFIG['sender_email'],
                     env.EMAIL_DELIVER_CONFIG['password'])
        server.sendmail(env.EMAIL_DELIVER_CONFIG['sender_email'],
                        env.EMAIL_DELIVER_CONFIG['receiver_email'],
                        message.as_string())


async def send_to_telegram(msg: model.Message):
    try:
        text = html.escape(gen_complex_text(msg))
        bot = telegram.Bot(token=env.TELEGRAM_BOT_TOKEN)
        async with bot:
            await bot.send_message(chat_id=env.TELEGRAM_CHAT_ID,
                                   text=text,
                                   parse_mode='html')
    except Exception as e:
        utils.print_with_time(f'Error occurs when send to telegram: {e}', True)


async def send_to_console(msg: model.Message):
    utils.print_with_time(f'Console: {msg}')


strategy_map = {
    consts.ENUM_DELIVER_TARGET_DEFAULT: send_to_console,
    consts.ENUM_DELIVER_TARGET_BARK: send_to_bark,
    consts.ENUM_DELIVER_TARGET_EMAIL: send_to_email,
    consts.ENUM_DELIVER_TARGET_TELEGRAM: send_to_telegram
}


async def deliver(msg: model.Message):
    tasks = []
    for target in consts.DELIVER_STRATEGY_PATTERN_BY_MSG_TYPE[msg.msg_type]:
        try:
            task = strategy_map.get(target)(msg)
            tasks.append(task)
        except Exception as e:
            utils.print_with_time(f'Error occurs when {target} deliver: {e}', True)
            return False
    await asyncio.gather(*tasks)
    return True


def execute(msg: model.Message):
    utils.safe_run(deliver(msg))
    return True
