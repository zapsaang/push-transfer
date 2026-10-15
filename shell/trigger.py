import asyncio
import email
import imaplib
import json
import time
from concurrent.futures import ThreadPoolExecutor
from email.header import decode_header

import consts
import deliver
import env
import model
import process
import utils


def run_loop(loop):
    asyncio.set_event_loop(loop)
    loop.run_forever()


def start_background_loop():
    loop = asyncio.new_event_loop()
    executor = ThreadPoolExecutor(max_workers=1)
    executor.submit(run_loop, loop)
    return loop


deliver_loop = start_background_loop()


def trans(json_data):
    try:
        msg = model.Message(**json_data)
        if not process.execute(msg):
            return
        asyncio.run_coroutine_threadsafe(deliver.deliver(msg), deliver_loop)
    except Exception as e:
        utils.print_with_time(e, True)


def handle_email(msg_data):
    ignore = False
    content_data = ''
    subject = ''
    for response_part in msg_data:
        if isinstance(response_part, tuple):
            msg = email.message_from_bytes(response_part[1])
            subject, encoding = decode_header(msg['Subject'])[0]
            if isinstance(subject, bytes):
                subject = subject.decode(encoding if encoding else 'utf-8')
            if subject != 'PushTransfer Email Trigger':
                ignore = True
            body = ''
            if msg.is_multipart():
                for part in msg.walk():
                    content_type = part.get_content_type()
                    content_disposition = str(part.get('Content-Disposition'))

                    try:
                        body = part.get_payload(decode=True).decode()
                    except Exception as e:
                        utils.print_with_time(e, True)

                    if content_type == 'text/plain' and 'attachment' not in content_disposition:
                        content_data = body
                        break
            else:
                body = msg.get_payload(decode=True).decode()
                content_data = body
    if not ignore:
        return content_data.strip()
    return ''


def _waiting_for_email_sync():
    while True:
        utils.print_with_time('start email trigger!')
        try:
            imap_connection = imaplib.IMAP4_SSL(host=env.EMAIL_TRIGGER_CONFIG['imap_server'],
                                                port=env.EMAIL_TRIGGER_CONFIG['imap_port'])
            imap_connection.login(env.EMAIL_TRIGGER_CONFIG['login_email'],
                                  env.EMAIL_TRIGGER_CONFIG['password'])
            while True:
                imap_connection.select()
                status, data = imap_connection.search('utf-8', 'UNSEEN')
                mail_list = data[0].split()
                for mail_id in mail_list:
                    status, msg_data = imap_connection.fetch(mail_id, '(RFC822)')
                    content_data = handle_email(msg_data)
                    if content_data is not None:
                        if content_data != '':
                            trans(json.loads(content_data))
                        utils.print_with_time(f'mail_id: {mail_id} handled')
                        imap_connection.store(mail_id, '+FLAGS', '\\Seen')
                time.sleep(1)
        except Exception as e:
            utils.print_with_time(e, True)
            utils.print_with_time('restarting email trigger!')


async def waiting_for_email():
    await utils.async_wrap(_waiting_for_email_sync)


strategy_map = {
    consts.ENUM_TRIGGER_EMAIL: waiting_for_email
}


async def execute():
    tasks = []
    for trigger in consts.TRIGGER_STRATEGY_PATTERN:
        try:
            task = strategy_map.get(trigger)()
            tasks.append(task)
        except Exception as e:
            utils.print_with_time(f'Error occurs when {trigger}: {e}', True)
            return False
    await asyncio.gather(*tasks)
    return True
