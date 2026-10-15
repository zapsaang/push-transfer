import re

import consts
import env
import model
import utils


class ProcessStrategy:
    def process(self, msg: model.Message):
        raise NotImplementedError("ProcessStrategy.process() must be overridden.")


class ProcessStrategyFilter(ProcessStrategy):
    def process(self, msg: model.Message):
        if msg.content is None:
            return True
        for regex in env.REGEX_FILTER_WHITE:
            if re.search(regex, msg.content):
                return True
        for regex in env.REGEX_FILTER_BLACK:
            if not re.search(regex, msg.content):
                continue
            return False
        return True


class ProcessStrategyCaptcha(ProcessStrategy):
    def process(self, msg: model.Message):
        if msg.content is None:
            return True
        for regex in env.REGEX_CAPTCHA:
            captcha_code = re.search(regex, msg.content)
            if not captcha_code:
                continue
            msg.set_captcha(captcha_code.group())
            return True
        return True


class ProcessStrategyEmergency(ProcessStrategy):
    def process(self, msg: model.Message):
        if msg.content is None:
            return True
        for regex in env.REGEX_EMERGENCY:
            emergency = re.search(regex, msg.content)
            if not emergency:
                continue
            msg.set_emergency(True)
            return True
        return True


class DefaultProcessStrategy(ProcessStrategy):
    def process(self, msg: model.Message):
        utils.print_with_time(f'Info: default process: {msg}')
        return True


strategy_map = {
    consts.ENUM_DELIVER_TARGET_DEFAULT: DefaultProcessStrategy(),
    consts.ENUM_PROCESS_STRATEGY_FILTER: ProcessStrategyFilter(),
    consts.ENUM_PROCESS_STRATEGY_CAPTCHA: ProcessStrategyCaptcha(),
    consts.ENUM_PROCESS_STRATEGY_EMERGENCY: ProcessStrategyEmergency()
}


def execute(msg: model.Message):
    for process in consts.PROCESS_STRATEGY_PATTERN_BY_MSG_TYPE[msg.msg_type]:
        try:
            if not strategy_map.get(process).process(msg):
                utils.print_with_time(f'Error: refused by {process}\nmsg.content: {msg.content}', True)
                return False
        except Exception as e:
            utils.print_with_time(f'Error occurs when {process}: {e}\nmsg: {msg}', True)
            return False
    return True
