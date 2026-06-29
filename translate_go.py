#!/usr/bin/env python3
"""ainovel-cli Go source Việt hóa script - xử lý Unicode đúng"""

import os
import re

ROOT = r"C:\Users\tunx8\.openclaw\workspace-main\ainovel-cli-fork"

# Bảng ánh xạ: Tiếng Trung → Tiếng Việt
# Sắp xếp theo độ dài giảm dần để thay chính xác trước
REPLACEMENTS = [
    # Long phrases first
    ("正在初始化", "Đang khởi tạo"),
    ("正在审阅", "Đang审阅"),
    ("正在规划", "Đang规划"),
    ("正在写入", "Đang ghi"),
    ("正在读取", "Đang đọc"),
    ("正在生成", "Đang tạo"),
    ("正在提交", "Đang nộp"),
    ("正在保存", "Đang lưu"),
    ("正在加载", "Đang tải"),
    ("正在导出", "Đang xuất"),
    ("正在导入", "Đang nhập"),
    ("正在分析", "Đang phân tích"),
    ("正在编辑", "Đang sửa"),
    ("正在检查", "Đang kiểm tra"),
    ("正在运行", "Đang chạy"),
    ("正在等待", "Đang chờ"),
    ("准备就绪", "Sẵn sàng"),
    ("等待输入", "Chờ nhập"),
    ("等待用户", "Chờ người dùng"),
    ("等待确认", "Chờ xác nhận"),
    ("未定书名", "Chưa đặt tên sách"),
    ("按回车确认", "Nhấn Enter xác nhận"),
    ("按ESC取消", "Nhấn ESC hủy"),
    ("已是最新版本", "Đã là bản mới nhất"),
    ("版本检查失败", "Kiểm tra phiên bản thất bại"),
    ("请选择模型提供商", "Vui lòng chọn nhà cung cấp mô hình"),
    ("请输入API密钥", "Vui lòng nhập khóa API"),
    ("请选择模型", "Vui lòng chọn mô hình"),
    ("确认配置", "Xác nhận cấu hình"),
    ("保存配置", "Lưu cấu hình"),
    ("测试连接", "Kiểm tra kết nối"),
    ("连接成功", "Kết nối thành công"),
    ("连接失败", "Kết nối thất bại"),
    ("首次使用", "Lần đầu sử dụng"),
    ("欢迎使用", "Chào mừng"),
    ("配置文件", "Tập tin cấu hình"),
    ("模型列表", "Danh sách mô hình"),
    ("上下文窗口", "Cửa sổ ngữ cảnh"),
    ("不在项目目录", "Không trong thư mục dự án"),
    ("更新可用", "Có bản cập nhật"),
    ("导入已有小说", "Nhập tiểu thuyết có sẵn"),
    ("风格模拟", "Mô phỏng phong cách"),
    ("查看统计", "Xem thống kê"),
    ("切换模型", "Đổi mô hình"),
    ("命令面板", "Bảng lệnh"),
    ("新小说", "Tiểu thuyết mới"),
    ("继续写作", "Tiếp tục viết"),
    ("删减篇幅", "Giảm篇幅"),
    ("重写章节", "Viết lại chương"),
    ("润色章节", "Đánh bóng chương"),
    ("导出为", "Xuất dạng"),
    ("纯文本", "Văn bản thuần"),
    ("电子书", "Sách điện tử"),
    ("标记语言", "Ngôn ngữ đánh dấu"),
    ("版本信息", "Thông tin phiên bản"),
    ("章节摘要", "Tóm tắt chương"),
    ("关键事件", "Sự kiện trọng tâm"),
    ("时间线事件", "Sự kiện thời gian"),
    ("伏笔更新", "Cập nhật伏笔"),
    ("关系变化", "Thay đổi quan hệ"),
    ("状态变化", "Thay đổi trạng thái"),
    ("钩子类型", "Loại móc"),
    ("主导线索", "Line chủ đạo"),
    ("请选择", "Vui lòng chọn"),
    ("请输入", "Vui lòng nhập"),
    ("格式错误", "Lỗi định dạng"),
    ("更新失败", "Cập nhật thất bại"),
    ("未指定", "Chưa chỉ định"),
    ("不支持", "Không hỗ trợ"),
    ("不允许", "Không允 phép"),
    ("不可用", "Không可用"),
    ("无效的", "Không hợp lệ"),
    ("未找到", "Không tìm thấy"),
    ("已存在", "Đã tồn tại"),
    ("已取消", "Đã hủy"),
    ("已跳过", "Đã bỏ qua"),
    ("运行中", "Đang chạy"),
    ("进行中", "Đang thực hiện"),
    ("待处理", "Chờ xử lý"),
    ("已完成", "Đã hoàn thành"),
    ("详细信息", "Chi tiết"),
    # Short words
    ("一致性", "Nhất quán"),
    ("审阅", "审阅"),
    ("规划", "规划"),
    ("写作", "Viết"),
    ("编辑", "Sửa"),
    ("读取", "Đọc"),
    ("导入", "Nhập"),
    ("导出", "Xuất"),
    ("恢复", "Phục hồi"),
    ("备份", "Sao lưu"),
    ("还原", "Hoàn nguyên"),
    ("快照", "Chụp"),
    ("检查", "Kiểm tra"),
    ("提交", "Nộp"),
    ("保存", "Lưu"),
    ("创建", "Tạo"),
    ("版本", "Phiên bản"),
    ("配置", "Cấu hình"),
    ("设置", "Thiết lập"),
    ("模型", "Mô hình"),
    ("提供商", "Nhà cung cấp"),
    ("文件", "Tập tin"),
    ("目录", "Thư mục"),
    ("路径", "Đường dẫn"),
    ("密钥", "Khóa"),
    ("端点", "Endpoint"),
    ("令牌", "Token"),
    ("代理", "Proxy"),
    ("上下文", "Ngữ cảnh"),
    ("窗口", "Cửa sổ"),
    ("书名", "Tên sách"),
    ("章节", "Chương"),
    ("大纲", "Đại cương"),
    ("人物", "Nhân vật"),
    ("世界观", "Thế giới quan"),
    ("伏笔", "伏笔"),
    ("草稿", "Bản nháp"),
    ("正文", "Chính văn"),
    ("标题", "Tiêu đề"),
    ("摘要", "Tóm tắt"),
    ("场景", "Cảnh"),
    ("进度", "Tiến độ"),
    ("状态", "Trạng thái"),
    ("错误", "Lỗi"),
    ("警告", "Cảnh báo"),
    ("失败", "Thất bại"),
    ("成功", "Thành công"),
    ("暂停", "Tạm dừng"),
    ("继续", "Tiếp tục"),
    ("取消", "Hủy"),
    ("确认", "Xác nhận"),
    ("退出", "Thoát"),
    ("选择", "Chọn"),
    ("输入", "Nhập"),
    ("搜索", "Tìm kiếm"),
    ("刷新", "Làm mới"),
    ("重试", "Thử lại"),
    ("跳过", "Bỏ qua"),
    ("默认", "Mặc định"),
    ("自定义", "Tuỳ chỉnh"),
    ("重置", "Đặt lại"),
    ("应用", "Áp dụng"),
    ("概览", "Tổng quan"),
    ("展开", "Mở rộng"),
    ("折叠", "Thu gọn"),
    ("启用", "Bật"),
    ("禁用", "Tắt"),
    ("完成", "Hoàn thành"),
    ("全部", "Tất cả"),
    ("部分", "Phần"),
    ("开始", "Bắt đầu"),
    ("结束", "Kết thúc"),
    ("返回", "Quay lại"),
    ("上一步", "Bước trước"),
    ("下一步", "Bước tiếp"),
    ("当前", "Hiện tại"),
    ("历史", "Lịch sử"),
    ("总计", "Tổng cộng"),
    ("剩余", "Còn lại"),
    ("未知", "Không rõ"),
    ("其他", "Khác"),
    ("试图", "Cố gắng"),
    ("检测到", "Phát hiện"),
    ("缺少", "Thiếu"),
    ("无法", "Không thể"),
    ("正在", "Đang"),
    ("请", "Vui lòng"),
    ("新", "Mới"),
    ("旧", "Cũ"),
    ("无", "Không có"),
    ("空", "Rỗng"),
    ("是否", "Có czy không"),
    ("需要", "Cần"),
    ("包括", "Bao gồm"),
    ("跳过", "Bỏ qua"),
    ("跳过", "Bỏ qua"),
    ("返回", "Trả về"),
    ("结果", "Kết quả"),
]

# CJK character range for detecting Chinese
CJK_RE = re.compile(r'[\u4e00-\u9fff]')

total_replaced = 0
total_files = 0

for dirpath, dirnames, filenames in os.walk(ROOT):
    # Skip .git
    if '.git' in dirpath:
        continue
    for fn in filenames:
        if not fn.endswith('.go'):
            continue
        fp = os.path.join(dirpath, fn)
        try:
            with open(fp, 'r', encoding='utf-8') as f:
                content = f.read()
        except (UnicodeDecodeError, PermissionError):
            continue
        
        if not CJK_RE.search(content):
            continue
        
        original = content
        for cn, vi in REPLACEMENTS:
            count = content.count(cn)
            if count > 0:
                content = content.replace(cn, vi)
                total_replaced += count
        
        if content != original:
            with open(fp, 'w', encoding='utf-8') as f:
                f.write(content)
            total_files += 1
            rel = os.path.relpath(fp, ROOT)
            print(f"OK {rel}")

print(f"\nTổng: {total_replaced} thay thế trong {total_files} files")
