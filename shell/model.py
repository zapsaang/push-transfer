class Message:
    def __init__(self, **kwargs):
        self.emergency = False
        self.captcha_code = None
        self.timestamp = None
        self.send_time = None
        self.receive_time = None
        self.transmit_time = None
        self.incoming_time = None
        self.battery_level = None
        self.content = None
        self.title = None
        self.msg_type = None
        for key, value in kwargs.items():
            setattr(self, key, value)

    def __str__(self):
        return f"Message({self.msg_type}, {self.title}, {self.content}, {self.timestamp})"

    def set_captcha(self, captcha_code):
        self.captcha_code = captcha_code

    def set_emergency(self, emergency):
        self.emergency = emergency

    # return the date and time of the message incoming time
    def get_incoming_time(self):
        return
